package main

// `loc read` against a seat carrying the DEPLOYMENT'S OWN access control.
//
// The presence suite authorises its seats with the block the presence model
// needs, which already grants a cursor on the seat's own queue. What is pinned
// here is the deployment as providers/nats/bootstrap.sh actually writes it —
// and the verb's answer when that block refuses the cursor.
//
// The shape of a failure is the whole point: non-zero, the reason on standard
// error, and NOTHING on standard out. A heading printed and then abandoned
// would tell a reader its mailbox is a place that was just looked at, which is
// exactly the false reading these cases exist to prevent.

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natsgo "github.com/nats-io/nats.go"

	"github.com/tdebasis/locutorium/internal/loctest"
	locpresence "github.com/tdebasis/locutorium/internal/presence"
)

const (
	aclPassword = "acl-cli-scratch"
	aclSeat     = "workshop.scribe"
)

// aclShippedSeat is the per-seat template of providers/nats/bootstrap.sh AS
// SHIPPED at 213338b — the script's lines 61-73, copied verbatim with $name
// substituted and nothing else changed:
//
//	publish: { allow: [
//	  "queue.*", "topic.>",
//	  "$JS.ACK.QUEUE_$name.>", "$JS.ACK.TOPICS.$name.>",
//	  "$JS.API.INFO",
//	  "$JS.API.STREAM.INFO.*", "$JS.API.STREAM.NAMES", "$JS.API.STREAM.LIST",
//	  "$JS.API.STREAM.SUBJECTS.TOPICS",
//	  "$JS.API.CONSUMER.INFO.>",
//	  "$JS.API.CONSUMER.MSG.NEXT.QUEUE_$name.$name",
//	  "$JS.API.CONSUMER.MSG.NEXT.TOPICS.$name",
//	  "$JS.API.CONSUMER.DURABLE.CREATE.TOPICS.$name",
//	  "$JS.API.CONSUMER.CREATE.TOPICS.$name", "$JS.API.CONSUMER.CREATE.TOPICS.$name.>" ] },
//	subscribe: { allow: ["queue.$name", "topic.>", "_INBOX.>"] } } }
//
// Not one of those names is the cursor the reader makes on its own queue.
func aclShippedSeat(name string) *natsserver.User {
	return &natsserver.User{Username: name, Password: aclPassword, Permissions: &natsserver.Permissions{
		Publish: &natsserver.SubjectPermission{Allow: []string{
			"queue.*", "topic.>",
			"$JS.ACK.QUEUE_" + name + ".>", "$JS.ACK.TOPICS." + name + ".>",
			"$JS.API.INFO",
			"$JS.API.STREAM.INFO.*", "$JS.API.STREAM.NAMES", "$JS.API.STREAM.LIST",
			"$JS.API.STREAM.SUBJECTS.TOPICS",
			"$JS.API.CONSUMER.INFO.>",
			"$JS.API.CONSUMER.MSG.NEXT.QUEUE_" + name + "." + name,
			"$JS.API.CONSUMER.MSG.NEXT.TOPICS." + name,
			"$JS.API.CONSUMER.DURABLE.CREATE.TOPICS." + name,
			"$JS.API.CONSUMER.CREATE.TOPICS." + name, "$JS.API.CONSUMER.CREATE.TOPICS." + name + ".>",
		}},
		Subscribe: &natsserver.SubjectPermission{Allow: []string{"queue." + name, "topic.>", "_INBOX.>"}},
	}}
}

// aclGrantedSeat is the same block after the fix: every JetStream object name
// substitutes the dot, and the seat may create the cursor on its own queue.
func aclGrantedSeat(name string) *natsserver.User {
	s := strings.ReplaceAll(name, ".", "_")
	// The events plane, mirrored from the script: a seat publishes its own
	// instance's events when its name carries an instance, and follows the
	// plane as any endpoint may.
	events := "presence.>"
	if i := strings.IndexByte(name, '.'); i >= 0 {
		events = "presence." + name[:i]
	}
	return &natsserver.User{Username: name, Password: aclPassword, Permissions: &natsserver.Permissions{
		Publish: &natsserver.SubjectPermission{Allow: []string{
			"queue.*", "queue.*.*", "topic.>", events,
			"$JS.ACK.QUEUE_" + s + ".>", "$JS.ACK.TOPICS." + s + ".>",
			"$JS.API.INFO",
			"$JS.API.STREAM.INFO.*", "$JS.API.STREAM.NAMES", "$JS.API.STREAM.LIST",
			"$JS.API.STREAM.SUBJECTS.TOPICS",
			"$JS.API.CONSUMER.INFO.>",
			"$JS.API.CONSUMER.MSG.NEXT.QUEUE_" + s + "." + s,
			"$JS.API.CONSUMER.MSG.NEXT.TOPICS." + s,
			"$JS.API.CONSUMER.CREATE.QUEUE_" + s + "." + s,
			"$JS.API.CONSUMER.DURABLE.CREATE.QUEUE_" + s + "." + s,
			"$JS.API.CONSUMER.DURABLE.CREATE.TOPICS." + s,
			"$JS.API.CONSUMER.CREATE.TOPICS." + s, "$JS.API.CONSUMER.CREATE.TOPICS." + s + ".>",
		}},
		Subscribe: &natsserver.SubjectPermission{Allow: []string{"queue." + name, "topic.>", "presence.>", "_INBOX.>"}},
	}}
}

// aclDeployment stands one scratch deployment up with the given seat block and
// seeds the seat's queue with one message — a stream and NO CONSUMER, which is
// the state a presence-model subscribe leaves behind.
func aclDeployment(t *testing.T, seat *natsserver.User, body string) {
	t.Helper()
	home := t.TempDir()
	users := []*natsserver.User{{Username: "admin", Password: aclPassword}, seat}
	srv := loctest.Boot(t, users, false)

	loctest.Write(t, filepath.Join(home, "config"), "provider = nats\nnats_url = "+srv.URL+"\n")
	loctest.Write(t, filepath.Join(home, "endpoints"), seat.Username+"\n")
	for _, u := range users {
		loctest.Write(t, filepath.Join(home, "creds", u.Username), aclPassword)
	}
	t.Setenv("LOC_HOME", home)
	t.Setenv("LOC_IDENTITY", seat.Username)
	// The spy provider from main_test.go is registered under "spy"; this
	// deployment names "nats", so it is never selected. Cleared defensively.
	installed = nil

	nc, js := srv.Admin(t, "admin", aclPassword)
	t.Cleanup(nc.Close)
	if _, err := js.AddStream(&natsgo.StreamConfig{
		Name:      locpresence.StreamName(seat.Username),
		Subjects:  []string{"queue." + seat.Username},
		Retention: natsgo.WorkQueuePolicy,
		Storage:   natsgo.MemoryStorage,
		Replicas:  1,
	}); err != nil {
		t.Fatalf("seed queue stream: %v", err)
	}
	env := `{"id":"` + body + `","ts":"2026-01-14T09:00:00.000Z","from":"host","to":"` +
		seat.Username + `","kind":"msg","body":"` + body + `"}`
	if err := nc.Publish("queue."+seat.Username, []byte(env)); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if err := nc.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
}

// UNDER THE SHIPPED ACCESS CONTROL, `read` MUST NOT PRINT AN EMPTY MAILBOX.
//
// The mail is in the stream. The cursor is refused, and a refusal arrives as
// silence — so before this the verb printed its two headings and exited 0 with
// the mail still stored, which is the reading a reader cannot tell apart from
// having lost it.
func TestReadFailsLoudWhenTheDeploymentRefusesTheSeatsOwnCursor(t *testing.T) {
	aclDeployment(t, aclShippedSeat(aclSeat), "shipped-acl-canary")

	code, out, errOut := exec("read")
	if code == 0 {
		t.Errorf("exit 0 on a refused cursor; stdout %q", out)
	}
	if out != "" {
		t.Errorf("a refused read printed a mailbox: %q", out)
	}
	if !regexp.MustCompile("(?i)refused|permissions|no cursor").MatchString(errOut) {
		t.Errorf("stderr does not say what was refused: %q", errOut)
	}
	if !strings.HasPrefix(errOut, "loc: cannot read queue."+aclSeat+":") {
		t.Errorf("the refusal is not in the verb's shape: %q", errOut)
	}
	if !strings.Contains(errOut, "providers/nats/bootstrap.sh") {
		t.Errorf("the refusal does not say where the fix lives: %q", errOut)
	}
}

// The other half of the pair: with the grant the deployment is supposed to
// carry, the very same invocation reads the message and exits 0. Without this,
// the case above would pass on a build that failed every read.
func TestReadSucceedsWhenTheDeploymentGrantsTheSeatsOwnCursor(t *testing.T) {
	aclDeployment(t, aclGrantedSeat(aclSeat), "granted-acl-canary")

	code, out, errOut := exec("read")
	if code != 0 || errOut != "" {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if !strings.HasPrefix(out, "── queue."+aclSeat+" ──\n") {
		t.Errorf("the reading is not headed by the queue it is: %q", out)
	}
	if !strings.Contains(out, "granted-acl-canary") {
		t.Errorf("the granted cursor read an empty mailbox: %q", out)
	}
}
