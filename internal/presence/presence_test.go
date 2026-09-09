package presence

import (
	"encoding/json"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ------------------------------------------------------------------- names

// The character set is not decoration: it is what makes the dots-to-
// underscores substitution injective, so no two endpoints can ever collide on
// one backing object.
func TestEndpointNames(t *testing.T) {
	good := []string{"workshop.scribe", "a.b", "atelier-two.clerk-3", "0.9"}
	for _, e := range good {
		if err := ValidEndpoint(e); err != nil {
			t.Errorf("%q was refused: %v", e, err)
		}
	}
	bad := []string{
		"",                 // nothing is not a name
		"scribe",           // a bare agent name is never an endpoint
		"workshop.scr_ibe", // the underscore is barred
		"workshop.a.b",     // exactly one dot
		"Workshop.scribe",  // no capitals
		"workshop.",        // both segments are required
		".scribe",
		"workshop scribe",
		"workshop.scribe\n",
	}
	for _, e := range bad {
		err := ValidEndpoint(e)
		if err == nil {
			t.Errorf("%q was accepted", e)
			continue
		}
		if !strings.Contains(err.Error(), "invalid endpoint name") {
			t.Errorf("the refusal of %q does not say what is wrong: %v", e, err)
		}
	}
}

func TestInstanceNames(t *testing.T) {
	if err := ValidInstance("workshop"); err != nil {
		t.Errorf("workshop was refused: %v", err)
	}
	for _, i := range []string{"", "workshop.scribe", "Workshop", "work_shop"} {
		if err := ValidInstance(i); err == nil {
			t.Errorf("%q was accepted as an instance", i)
		}
	}
}

// The same agent name in two instances backs two DISTINCT objects. That is the
// collision made impossible by construction, rather than avoided by everyone
// choosing distinct names.
func TestBackingNamesAreInjective(t *testing.T) {
	if got, want := StreamName("workshop.scribe"), "QUEUE_workshop_scribe"; got != want {
		t.Errorf("StreamName = %q, want %q", got, want)
	}
	if StreamName("workshop.scribe") == StreamName("atelier.scribe") {
		t.Error("the same agent in two instances mapped to one backing object")
	}
	if got, want := Instance("workshop.scribe"), "workshop"; got != want {
		t.Errorf("Instance = %q, want %q", got, want)
	}
	if got := Instance("scribe"); got != "" {
		t.Errorf("Instance of an unqualified name = %q, want nothing", got)
	}
}

// ------------------------------------------------------------------ events

func TestTheTaxonomy(t *testing.T) {
	if len(Kinds()) != 7 {
		t.Errorf("the taxonomy has %d kinds, want the schema's seven", len(Kinds()))
	}
	for _, k := range Kinds() {
		if !ValidKind(k) {
			t.Errorf("%q is in the taxonomy but was not recognised", k)
		}
	}
	if ValidKind("activity.middle") {
		t.Error("a kind outside the taxonomy was recognised")
	}
	// Leaving is a membership fact, not a working one: recording it as
	// activity would leave a departed agent looking busy.
	for _, k := range []string{KindActivityStart, KindActivityEnd, KindToolPre, KindToolPost} {
		if !RecordsActivity(k) {
			t.Errorf("%q does not move the activity state, but it is work", k)
		}
	}
	for _, k := range []string{KindSubscribe, KindUnsubscribe} {
		if RecordsActivity(k) {
			t.Errorf("%q moves the activity state, but it is membership", k)
		}
	}
	// The third subject is the house's own health. It is not work either, and
	// its own tests are in incident_test.go.
	if RecordsActivity(KindIncident) {
		t.Errorf("%q moves the activity state, but it is the house", KindIncident)
	}
}

// THE FIELD ORDER IS THE WIRE FORMAT, everything past the envelope is omitted
// when empty, and HTML escaping is off so a path with an angle bracket in it
// looks the same whichever emitter published it.
func TestEventMarshalling(t *testing.T) {
	ev := NewEvent(KindActivityStart, "workshop.scribe")
	ev.TS = "2026-01-14T09:31:20.114Z"
	ev.ID = "ev_0011223344556677"

	b, err := ev.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want := `{"id":"ev_0011223344556677","ts":"2026-01-14T09:31:20.114Z","kind":"activity.start","endpoint":"workshop.scribe"}`
	if string(b) != want {
		t.Errorf("light event:\n got %s\nwant %s", b, want)
	}

	ev.Instance = "workshop"
	ev.Kind = KindSubscribe
	ev.Agent = &Agent{Type: "acme-cli", Version: "3.2.0"}
	ev.Process = &Process{PID: 4242, Started: "2026-01-14T09:12:04.006Z"}
	ev.Display = &Display{Name: "The Scribe"}
	ev.Cwd = "/workspaces/<scribe>"
	b, err = ev.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want = `{"id":"ev_0011223344556677","ts":"2026-01-14T09:31:20.114Z","kind":"agent.subscribe",` +
		`"endpoint":"workshop.scribe","instance":"workshop","agent":{"type":"acme-cli","version":"3.2.0"},` +
		`"process":{"pid":4242,"started":"2026-01-14T09:12:04.006Z"},"display":{"name":"The Scribe"},` +
		`"cwd":"/workspaces/<scribe>"}`
	if string(b) != want {
		t.Errorf("join event:\n got %s\nwant %s", b, want)
	}
}

// An id is what deduplication and a reference both rest on, so two must never
// be the same, and the stamp is millisecond and UTC whatever the clock has.
func TestIdsAndStamps(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		id := NewID()
		if !strings.HasPrefix(id, "ev_") || len(id) != len("ev_")+16 {
			t.Fatalf("id %q is not the shape the schema fixes", id)
		}
		if seen[id] {
			t.Fatalf("id %q came up twice", id)
		}
		seen[id] = true
	}
	if _, err := time.Parse(time.RFC3339, Now()); err != nil {
		t.Errorf("Now() is not RFC 3339: %v", err)
	}
	if !strings.HasSuffix(Now(), "Z") || len(Now()) != len(TSLayout) {
		t.Errorf("Now() = %q, want the schema's UTC millisecond stamp", Now())
	}
}

// ------------------------------------------------------------------ ledger

func scratch(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("LOC_HOME", home)
	return home
}

func reg(endpoint string, pid int, started string) *Registration {
	return &Registration{
		Endpoint:   endpoint,
		Instance:   Instance(endpoint),
		Agent:      Agent{Type: "acme-cli", Version: "3.2.0"},
		Process:    Process{PID: pid, Started: started},
		Registered: "2026-01-14T09:12:04.318Z",
	}
}

// A free endpoint is one of the two answers, not an error.
func TestLoadAndSaveARegistration(t *testing.T) {
	scratch(t)

	got, err := Load("workshop.scribe")
	if err != nil || got != nil {
		t.Fatalf("a free endpoint gave (%v, %v), want nothing and no error", got, err)
	}
	if err := Save(reg("workshop.scribe", 4242, "2026-01-14T09:12:04.006Z")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err = Load("workshop.scribe")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got == nil || got.Process.PID != 4242 || got.Agent.Type != "acme-cli" {
		t.Errorf("round trip lost the registration: %+v", got)
	}
	// Everything the ledger holds goes with the subscription: nothing outlives
	// it, activity included.
	if _, err := ApplyActivity("workshop.scribe", "2026-01-14T09:31:20.114Z", KindActivityStart); err != nil {
		t.Fatalf("ApplyActivity: %v", err)
	}
	if err := Remove("workshop.scribe"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if got, _ := Load("workshop.scribe"); got != nil {
		t.Error("the registration survived its removal")
	}
	if act, _ := LoadActivity("workshop.scribe"); act != nil {
		t.Error("the activity record survived the subscription")
	}
	// Removing what is already gone is what the caller asked for.
	if err := Remove("workshop.scribe"); err != nil {
		t.Errorf("removing an absent registration: %v", err)
	}
}

// Half a record is reported, not read as an empty one: an endpoint that may be
// held must never look free.
func TestAnUnreadableRecordIsReported(t *testing.T) {
	home := scratch(t)
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(Dir(), "workshop.scribe.json"), []byte("half a"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load("workshop.scribe"); err == nil {
		t.Error("half a registration was accepted")
	}
	if err := os.WriteFile(filepath.Join(Dir(), "workshop.scribe.activity.json"), []byte("half a"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadActivity("workshop.scribe"); err == nil {
		t.Error("half an activity record was accepted")
	}
	// A record area that is not a directory is a broken deployment, and it is
	// said so rather than read as empty.
	other := scratch(t)
	_ = other
	if err := os.MkdirAll(filepath.Dir(Dir()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Dir(), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := List("workshop"); err == nil {
		t.Error("a record area that is not a directory listed cleanly")
	}
	if err := Save(reg("workshop.scribe", 1, "")); err == nil {
		t.Error("a registration was written into a file")
	}
	_ = home
}

// A record that cannot even be opened is reported too — a deployment whose run
// area has been trampled on says so rather than answering "nobody is here",
// which is the one wrong answer these three facts exist to avoid.
func TestARecordThatCannotBeOpenedIsReported(t *testing.T) {
	scratch(t)
	// A directory where a record belongs: readable as an entry, unreadable as
	// a file, and not something to mistake for an absent registration.
	for _, name := range []string{"workshop.scribe.json", "workshop.scribe.activity.json"} {
		if err := os.MkdirAll(filepath.Join(Dir(), name, "inside"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Load("workshop.scribe"); err == nil {
		t.Error("a registration that could not be opened read as an absent one")
	}
	if _, err := LoadActivity("workshop.scribe"); err == nil {
		t.Error("an activity record that could not be opened read as an absent one")
	}
	if _, err := ApplyActivity("workshop.scribe", "2026-01-14T10:04:30.000Z", KindActivityEnd); err == nil {
		t.Error("an event was applied over a record that could not be read")
	}
	if err := Remove("workshop.scribe"); err == nil {
		t.Error("a record that could not be removed reported success")
	}
}

// A listing is per instance, in order, and a record it cannot read is skipped
// rather than stopping it — a sweep that halts at the first bad file leaves
// the rest of the dead in place.
func TestListIsPerInstanceAndOrdered(t *testing.T) {
	scratch(t)

	if got, err := List("workshop"); err != nil || got != nil {
		t.Fatalf("with no ledger at all: (%v, %v), want nothing and no error", got, err)
	}
	for _, e := range []string{"workshop.scribe", "workshop.clerk", "atelier.scribe"} {
		if err := Save(reg(e, 4242, "")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ApplyActivity("workshop.scribe", "2026-01-14T09:31:20.114Z", KindToolPre); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(Dir(), "workshop.broken.json"), []byte("half a"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(Dir(), "notes.txt"), []byte("ignored"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := List("workshop")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var names []string
	for _, r := range got {
		names = append(names, r.Endpoint)
	}
	want := []string{"workshop.clerk", "workshop.scribe"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("List = %v, want %v — one instance, in order, activity records and rubbish left out", names, want)
	}
}

// The load-bearing derivation rule: an OLDER event offered after a newer one is
// DROPPED. A start from 10:04:25 arriving after an end from 10:04:30 must not
// flip the agent back to working.
func TestStaleActivityIsDiscarded(t *testing.T) {
	scratch(t)

	if got, err := LoadActivity("workshop.scribe"); err != nil || got != nil {
		t.Fatalf("with nothing recorded: (%v, %v), want nothing and no error", got, err)
	}
	applied, err := ApplyActivity("workshop.scribe", "2026-01-14T10:04:30.000Z", KindActivityEnd)
	if err != nil || !applied {
		t.Fatalf("the first event was not applied: (%v, %v)", applied, err)
	}
	applied, err = ApplyActivity("workshop.scribe", "2026-01-14T10:04:25.000Z", KindActivityStart)
	if err != nil {
		t.Fatal(err)
	}
	if applied {
		t.Error("an older event was applied over a newer one")
	}
	act, err := LoadActivity("workshop.scribe")
	if err != nil || act == nil {
		t.Fatalf("LoadActivity: (%v, %v)", act, err)
	}
	if act.Kind != KindActivityEnd || act.TS != "2026-01-14T10:04:30.000Z" {
		t.Errorf("the record is %+v, want the newer end still standing", act)
	}
	// A newer one does land, and the same stamp again is not older.
	if applied, _ := ApplyActivity("workshop.scribe", "2026-01-14T10:04:30.000Z", KindToolPre); !applied {
		t.Error("an event with the same stamp was treated as older")
	}
	if applied, _ := ApplyActivity("workshop.scribe", "2026-01-14T10:05:00.000Z", KindActivityStart); !applied {
		t.Error("a newer event was not applied")
	}
	// An earlier moment written in another legal RFC 3339 offset is still
	// earlier: the stamps are compared as moments, which a byte comparison
	// would get wrong. 01:00-08:00 is 09:00Z, before the 10:05Z standing.
	if applied, _ := ApplyActivity("workshop.scribe", "2026-01-14T01:00:00.000-08:00", KindActivityEnd); applied {
		t.Error("an older moment written in another offset was applied")
	}
	// A stamp nothing can parse cannot be SHOWN to be older, so it is applied
	// rather than silently dropped — and a report reading it then says the
	// stamp is unreadable instead of inventing a state from it.
	if applied, _ := ApplyActivity("workshop.scribe", "whenever", KindActivityEnd); !applied {
		t.Error("a stamp that could not be parsed was treated as proven stale")
	}
	// With two stamps neither of which is a moment, the comparison falls back
	// to the text — the best answer available, rather than a crash.
	if applied, _ := ApplyActivity("workshop.scribe", "always", KindActivityStart); applied {
		t.Error("the fallback comparison accepted a lexically earlier stamp")
	}
}

// ---------------------------------------------------------------- liveness

// ps prints a ctime-like line on both the platforms this runs on. The day is
// space-padded to two columns for a single-digit day and unpadded otherwise,
// which is the only shape difference between them.
func TestStartTimesAreNormalised(t *testing.T) {
	cases := []struct{ name, lstart string }{
		{"a single-digit day, space padded", "Sat Sep  6 15:49:58 2026"},
		{"a two-digit day", "Wed Jan 14 09:12:04 2026"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeStart(tc.lstart)
			if got == "" {
				t.Fatalf("%q was not understood", tc.lstart)
			}
			if _, err := time.Parse(time.RFC3339, got); err != nil {
				t.Errorf("%q normalised to %q, which is not a stamp: %v", tc.lstart, got, err)
			}
			if len(got) != len(TSLayout) || !strings.HasSuffix(got, "Z") {
				t.Errorf("%q normalised to %q, want the schema's UTC millisecond stamp", tc.lstart, got)
			}
		})
	}
	// The line is read as local time, which is what ps meant by it.
	local := time.Date(2026, 1, 14, 9, 12, 4, 0, time.Local)
	if got, want := normalizeStart("Wed Jan 14 09:12:04 2026"), local.UTC().Format(TSLayout); got != want {
		t.Errorf("normalizeStart = %q, want %q — the line carries no zone, so it is the machine's", got, want)
	}
	// Whitespace either side is ps's, not the value's.
	if normalizeStart("  Wed Jan 14 09:12:04 2026 \n") != normalizeStart("Wed Jan 14 09:12:04 2026") {
		t.Error("surrounding whitespace changed the value")
	}
	for _, junk := range []string{"", "   ", "whenever", "2026-01-14T09:12:04Z"} {
		if got := normalizeStart(junk); got != "" {
			t.Errorf("normalizeStart(%q) = %q, want nothing", junk, got)
		}
	}
}

// THE PAIR IS THE IDENTITY. A number alone would go on looking alive the
// moment the operating system handed it to something else.
func TestAlivenessIsThePair(t *testing.T) {
	self := os.Getpid()
	started := StartedAt(self)
	if started == "" {
		t.Fatal("the operating system said nothing about the process asking")
	}
	if !Alive(self, started) {
		t.Error("a running pid with its own start time was not seen as alive")
	}
	if Alive(self, "2020-01-01T00:00:00.000Z") {
		t.Error("a mismatched start time was accepted — the number alone was enough")
	}
	if Alive(self, "") {
		t.Error("a registration with no start time was treated as alive")
	}
	for _, pid := range []int{0, -1} {
		if Alive(pid, started) {
			t.Errorf("pid %d was treated as a process", pid)
		}
	}

	// A process id that is certainly free: a child started and reaped.
	c := osexec.Command("sh", "-c", "exit 0")
	if err := c.Start(); err != nil {
		t.Fatalf("start throwaway process: %v", err)
	}
	dead := c.Process.Pid
	_ = c.Wait()
	if StartedAt(dead) != "" {
		t.Error("the operating system had a start time for a process that is gone")
	}
	if Alive(dead, started) {
		t.Error("a reaped pid was reported as alive")
	}

	// And the same question asked of a registration.
	if !reg("workshop.scribe", self, started).Alive() {
		t.Error("a live registration was not seen as alive")
	}
	if reg("workshop.scribe", dead, "").Alive() {
		t.Error("a registration whose start time was never readable was seen as alive")
	}
}

// --------------------------------------------------------------- derivation

// The three facts are reported SEPARATELY, each with the reason for it. Away
// because there is no process, idle because nothing has happened lately, and
// idle because the window elapsed are different situations, and collapsing
// them into one word hides which one is being looked at.
func TestDerivation(t *testing.T) {
	now := time.Date(2026, 1, 14, 10, 0, 0, 0, time.UTC)
	window := 10 * time.Minute
	self := os.Getpid()
	live := reg("workshop.scribe", self, StartedAt(self))
	dead := reg("workshop.scribe", 4242, "")

	cases := []struct {
		name        string
		reg         *Registration
		act         *Activity
		wantMatches []string
	}{
		{
			name:        "nothing registered",
			wantMatches: []string{"registered: no", "process:    unknown (no registration)", "activity:   unknown (no registration)"},
		},
		{
			name:        "registered, alive, nothing said",
			reg:         live,
			wantMatches: []string{"registered: yes (since 2026-01-14T09:12:04.318Z; acme-cli 3.2.0)", "process:    alive (pid ", "activity:   idle (no activity events)"},
		},
		{
			name:        "the process is gone",
			reg:         dead,
			act:         &Activity{TS: "2026-01-14T09:59:00.000Z", Kind: KindActivityStart},
			wantMatches: []string{"process:    gone (pid 4242, started unknown)", "activity:   away (process gone)"},
		},
		{
			name:        "an end event, and the transition is immediate",
			reg:         live,
			act:         &Activity{TS: "2026-01-14T09:59:00.000Z", Kind: KindActivityEnd},
			wantMatches: []string{"activity:   idle (activity.end at 2026-01-14T09:59:00.000Z)"},
		},
		{
			name:        "a tool call inside the window",
			reg:         live,
			act:         &Activity{TS: "2026-01-14T09:59:00.000Z", Kind: KindToolPre},
			wantMatches: []string{"activity:   active (tool.pre at 2026-01-14T09:59:00.000Z)"},
		},
		{
			name:        "work that began inside the window",
			reg:         live,
			act:         &Activity{TS: "2026-01-14T09:55:00.000Z", Kind: KindActivityStart},
			wantMatches: []string{"activity:   active (activity.start at 2026-01-14T09:55:00.000Z)"},
		},
		{
			name:        "the window has elapsed with nothing further",
			reg:         live,
			act:         &Activity{TS: "2026-01-14T09:30:00.000Z", Kind: KindToolPost},
			wantMatches: []string{"activity:   idle (idle window elapsed since 2026-01-14T09:30:00.000Z)"},
		},
		{
			name:        "an empty record is no record",
			reg:         live,
			act:         &Activity{},
			wantMatches: []string{"activity:   idle (no activity events)"},
		},
		{
			name:        "a stamp that cannot be read",
			reg:         live,
			act:         &Activity{TS: "whenever", Kind: KindToolPre},
			wantMatches: []string{`activity:   idle (unreadable timestamp "whenever" on the last tool.pre)`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Derive("workshop.scribe", tc.reg, tc.act, now, window)
			if !strings.HasPrefix(got, "workshop.scribe\n") {
				t.Errorf("the report does not name the endpoint it is about:\n%s", got)
			}
			for _, want := range tc.wantMatches {
				if !strings.Contains(got, want) {
					t.Errorf("the report does not carry %q:\n%s", want, got)
				}
			}
			// Every idle branch avoids the word the working state uses, so a
			// reader looking for it cannot find it in a line that means the
			// opposite.
			if strings.Contains(got, "idle") && strings.Contains(got, " active ") {
				t.Errorf("an idle report carries the working word:\n%s", got)
			}
		})
	}
}

// The registration written by a subscribe is the shape a consumer already
// knows: the join event's payload, and the stamp that dates it.
func TestARegistrationIsTheJoinPayloadShape(t *testing.T) {
	b, err := json.Marshal(reg("workshop.scribe", 4242, "2026-01-14T09:12:04.006Z"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"endpoint", "instance", "agent", "process", "registered"} {
		if _, ok := m[k]; !ok {
			t.Errorf("a registration carries no %q", k)
		}
	}
	if _, ok := m["display"]; ok {
		t.Error("an agent with no display name carries an empty one")
	}
}
