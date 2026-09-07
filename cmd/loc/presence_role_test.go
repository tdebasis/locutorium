package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// display.role — the second half of the join event's display object.
//
// docs/PRESENCE.md §Events gives agent.subscribe a
// "display": { "name": …, "role": … }, so an agent announces both what to call
// it and what it is for. The role is OPTIONAL: a deployment that has no use
// for one passes no --role, and the key is then absent rather than empty, so a
// consumer can tell "no role was given" from "the role is the empty string".
//
// The broker-backed cases reuse the presence deployment and the event witness
// from the integration suite; the rendering case uses the spy deployment,
// because printing a roster touches no medium.

func TestPresence_Subscribe_JoinEventCarriesTheDisplayRole(t *testing.T) {
	p := newPresence(t)
	p.as(t, "host")
	sub := p.witness(t, "presence.workshop")

	exec("subscribe", e2, "--pid", pidStr(livePid(t)), "--type", "acme-cli", "--version", "3.2.0",
		"--display", "The Clerk", "--role", "Records")

	events := collect(sub, 2*time.Second)
	m, ok := findEvent(t, events, "agent.subscribe")
	if !ok {
		t.Fatalf("subscribe published no agent.subscribe event on presence.workshop; saw %v", events)
	}
	display, _ := m["display"].(map[string]any)
	if display == nil {
		t.Fatalf("join event carries no display object; got %v", m["display"])
	}
	if display["name"] != "The Clerk" {
		t.Errorf("display.name = %v, want %q", display["name"], "The Clerk")
	}
	if display["role"] != "Records" {
		t.Errorf("display.role = %v, want %q", display["role"], "Records")
	}
}

// No --role, no key. The join-event shape a deployment that never uses roles
// sees is exactly the one it saw before.
func TestPresence_Subscribe_WithoutARoleOmitsTheKey(t *testing.T) {
	p := newPresence(t)
	p.as(t, "host")
	sub := p.witness(t, "presence.workshop")

	exec("subscribe", e2, "--pid", pidStr(livePid(t)), "--type", "acme-cli", "--version", "3.2.0",
		"--display", "The Clerk")

	events := collect(sub, 2*time.Second)
	m, ok := findEvent(t, events, "agent.subscribe")
	if !ok {
		t.Fatalf("subscribe published no agent.subscribe event on presence.workshop; saw %v", events)
	}
	display, _ := m["display"].(map[string]any)
	if display == nil {
		t.Fatalf("join event carries no display object; got %v", m["display"])
	}
	if _, present := display["role"]; present {
		t.Errorf("display carries a role key when none was given; got %v", display)
	}
}

// The roster is where a person reads the registration back, so a role that was
// registered has to appear there too.
func TestRegistryRenderingCarriesTheRole(t *testing.T) {
	d := newPresenceDeployment(t)
	d.spy.reply = []byte(`{"agents":[{"endpoint":"workshop.clerk","instance":"workshop",` +
		`"agent":{"type":"acme-cli","version":"3.2.0"},"process":{"pid":4242,"started":"2026-01-14T09:12:04.006Z"},` +
		`"display":{"name":"The Clerk","role":"Records"},` +
		`"cwd":"/workspaces/clerk","registered":"2026-01-14T09:12:04.318Z"}]}`)

	code, out, errOut := exec("registry", "workshop")
	if code != 0 || errOut != "" {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if !strings.Contains(out, "Records") {
		t.Errorf("roster %q does not carry the registered role %q", out, "Records")
	}
}

// The ledger keeps what the event announced, so a registry answered from it
// carries the role too.
func TestSubscribeRecordsTheRoleInTheLedger(t *testing.T) {
	p := newPresence(t)
	p.as(t, "host")

	exec("subscribe", e2, "--pid", pidStr(livePid(t)), "--type", "acme-cli", "--version", "3.2.0",
		"--display", "The Clerk", "--role", "Records")

	b, err := os.ReadFile(filepath.Join(p.home, "run", "presence", e2+".json"))
	if err != nil {
		t.Fatalf("read the registration: %v", err)
	}
	var reg map[string]any
	if err := json.Unmarshal(b, &reg); err != nil {
		t.Fatalf("registration is not readable: %v", err)
	}
	display, _ := reg["display"].(map[string]any)
	if display == nil || display["role"] != "Records" {
		t.Errorf("registration display = %v, want it to carry role %q", reg["display"], "Records")
	}
}
