package presence

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// full is a registration with every optional field supplied, so a case that
// removes one of them is testing that removal and nothing else.
func full(endpoint string) *Registration {
	return &Registration{
		Endpoint:   endpoint,
		Instance:   Instance(endpoint),
		Agent:      Agent{Type: "claude", Version: "2.1.278"},
		Process:    Process{PID: 4242, Started: "Sat Sep 19 01:33:49 2026"},
		Display:    &Display{Name: "Scribe", Role: "Records"},
		Cwd:        "/workspaces/scribe",
		Address:    "%2",
		Registered: "2026-09-19T08:33:49.102Z",
	}
}

// The whole report for one agent, line for line. Every other case here changes
// one thing about this and looks at what moved.
func TestOneAgentPrintsItsWholeRecord(t *testing.T) {
	got := Registry("", []*Registration{full("workshop.scribe")}, nil)
	want := "workshop\n" +
		"  workshop.scribe\n" +
		"    agent:      claude 2.1.278\n" +
		"    process:    pid 4242, started Sat Sep 19 01:33:49 2026\n" +
		"    registered: 2026-09-19T08:33:49.102Z\n" +
		"    address:    %2\n" +
		"    cwd:        /workspaces/scribe\n" +
		"    display:    Scribe (Records)\n"
	if got != want {
		t.Errorf("registry report:\n%s\nwant:\n%s", got, want)
	}
}

// Every agent sits under a house line, and the agents of one house are
// together. A flat list of endpoints would answer the same question and lose
// which deployment each seat belongs to.
func TestAgentsAreGroupedUnderTheirHouse(t *testing.T) {
	// Given out of order on purpose: the caller's order must not decide the
	// report's.
	rows := []*Registration{
		full("workshop.scribe"),
		full("atelier.clerk"),
		full("workshop.crier"),
	}
	got := Registry("", rows, nil)
	houses := headings(got)
	if strings.Join(houses, ",") != "atelier,workshop" {
		t.Errorf("house lines %v, want atelier then workshop", houses)
	}
	// Each agent must be INDENTED UNDER a house line and not merely somewhere
	// after it: the grouping is what the indentation claims.
	wantOrder := []string{"atelier", "  atelier.clerk", "workshop", "  workshop.crier", "  workshop.scribe"}
	if order := structure(got); strings.Join(order, "|") != strings.Join(wantOrder, "|") {
		t.Errorf("report structure %v, want %v", order, wantOrder)
	}
}

// Agents inside a house are ordered by endpoint, and an unreadable row takes
// its place in that order rather than being appended after the readable ones.
func TestAgentsInAHouseAreOrderedByEndpoint(t *testing.T) {
	rows := []*Registration{full("workshop.crier"), full("workshop.scribe")}
	bad := []Unreadable{{Endpoint: "workshop.mason", Err: errors.New("bad json")}}
	want := []string{"workshop", "  workshop.crier", "  workshop.mason", "  workshop.scribe"}
	if order := structure(Registry("", rows, bad)); strings.Join(order, "|") != strings.Join(want, "|") {
		t.Errorf("order %v, want %v", order, want)
	}
}

// A row that cannot be parsed is PRINTED, NOT SKIPPED. A listing that drops it
// reports a registered seat as absent, and the endpoint is readable from the
// file name even when nothing inside the file is.
func TestAnUnreadableRowIsPrintedAndNotSkipped(t *testing.T) {
	bad := []Unreadable{{Endpoint: "workshop.mason", Err: errors.New("the registration for 'workshop.mason' is not readable: unexpected end of JSON input")}}
	got := Registry("", nil, bad)
	want := "workshop\n" +
		"  workshop.mason\n" +
		"    unreadable: the registration for 'workshop.mason' is not readable: unexpected end of JSON input\n"
	if got != want {
		t.Errorf("unreadable row:\n%s\nwant:\n%s", got, want)
	}
}

// Every label prints for every agent, whatever the record holds. A label that
// disappears when its field is empty makes the reader count lines to learn
// which fact is missing.
func TestEveryLabelPrintsForEveryAgent(t *testing.T) {
	bare := &Registration{
		Endpoint:   "workshop.crier",
		Instance:   "workshop",
		Agent:      Agent{Type: "tmux", Version: "3.2.0"},
		Process:    Process{PID: 4311},
		Registered: "2026-09-19T08:33:49.318Z",
	}
	for _, rows := range [][]*Registration{{full("workshop.scribe")}, {bare}} {
		got := Registry("", rows, nil)
		for _, label := range []string{"agent:", "process:", "registered:", "address:", "cwd:", "display:"} {
			if !strings.Contains(got, "    "+label) {
				t.Errorf("label %q missing from:\n%s", label, got)
			}
		}
	}
}

// A field the agent never supplied prints `(not given)`. An empty value after
// the label would read as a name of no characters, which is a different fact.
func TestAFieldTheAgentDidNotGivePrintsNotGiven(t *testing.T) {
	bare := &Registration{
		Endpoint:   "workshop.crier",
		Instance:   "workshop",
		Agent:      Agent{Type: "tmux", Version: "3.2.0"},
		Process:    Process{PID: 4311},
		Registered: "2026-09-19T08:33:49.318Z",
	}
	got := Registry("", []*Registration{bare}, nil)
	for _, want := range []string{
		"    process:    pid 4311, started (not given)\n",
		"    address:    (not given)\n",
		"    cwd:        (not given)\n",
		"    display:    (not given)\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("want line %q in:\n%s", want, got)
		}
	}
	// No label may be followed by nothing at all.
	for _, l := range strings.Split(strings.TrimRight(got, "\n"), "\n") {
		if strings.HasSuffix(l, ":") || strings.HasSuffix(l, ": ") || strings.HasSuffix(l, " ") {
			t.Errorf("line %q ends with an empty value", l)
		}
	}
}

// The role is part of the display and it is optional. An agent that declared
// one is shown with it; an agent that declared none is shown without an empty
// pair of brackets.
func TestTheDisplayShowsTheRoleWhenThereIsOne(t *testing.T) {
	withRole := full("workshop.scribe")
	if got := Registry("", []*Registration{withRole}, nil); !strings.Contains(got, "    display:    Scribe (Records)\n") {
		t.Errorf("display with a role wrong:\n%s", got)
	}
	noRole := full("workshop.scribe")
	noRole.Display = &Display{Name: "Scribe"}
	got := Registry("", []*Registration{noRole}, nil)
	if !strings.Contains(got, "    display:    Scribe\n") {
		t.Errorf("display without a role wrong:\n%s", got)
	}
	if strings.Contains(got, "()") {
		t.Errorf("an absent role printed empty brackets:\n%s", got)
	}
}

// One argument limits the report to one house, and it drops the unreadable
// rows of every other house too.
func TestAHouseArgumentKeepsOnlyThatHouse(t *testing.T) {
	rows := []*Registration{full("atelier.clerk"), full("workshop.scribe")}
	bad := []Unreadable{{Endpoint: "atelier.mason", Err: errors.New("bad json")}}
	got := Registry("workshop", rows, bad)
	if strings.Contains(got, "atelier") {
		t.Errorf("another house leaked into the report:\n%s", got)
	}
	if !strings.Contains(got, "  workshop.scribe\n") {
		t.Errorf("the asked-for house is missing:\n%s", got)
	}
}

// An empty registry is an answer. The two wordings differ because "nobody is
// registered anywhere" and "nobody is registered in the house you named" are
// different facts.
func TestAnEmptyRegistryIsAnAnswer(t *testing.T) {
	if got := Registry("", nil, nil); got != "(no agents registered)\n" {
		t.Errorf("empty registry printed %q", got)
	}
	if got := Registry("atelier", []*Registration{full("workshop.scribe")}, nil); got != "(no agents registered in atelier)\n" {
		t.Errorf("empty house printed %q", got)
	}
}

// --json hands back the records as the ledger stores them. A consumer already
// reads this shape, and reshaping it here is a second format to learn.
func TestTheJSONHoldsTheStoredRecords(t *testing.T) {
	row := full("workshop.scribe")
	b, err := RegistryJSON("", []*Registration{row}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var back struct {
		Agents     []*Registration `json:"agents"`
		Unreadable []struct {
			Endpoint string `json:"endpoint"`
			Error    string `json:"error"`
		} `json:"unreadable"`
	}
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if len(back.Agents) != 1 {
		t.Fatalf("agents: %d, want 1", len(back.Agents))
	}
	stored, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(back.Agents[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(stored) {
		t.Errorf("record came back as %s, want the stored %s", got, stored)
	}
	// The unreadable key is absent when every row was read, so its presence is
	// itself the signal that something could not be.
	if strings.Contains(string(b), "unreadable") {
		t.Errorf("the unreadable key appeared with nothing unreadable: %s", b)
	}
	// An empty registry is an empty list, never a null.
	empty, err := RegistryJSON("", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(empty) != `{"agents":[]}` {
		t.Errorf("empty registry as JSON is %s", empty)
	}
}

// An unreadable row reaches the JSON consumer too, under its own key.
func TestTheJSONNamesAnUnreadableRow(t *testing.T) {
	bad := []Unreadable{{Endpoint: "workshop.mason", Err: errors.New("bad json")}}
	b, err := RegistryJSON("", nil, bad)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"agents":[],"unreadable":[{"endpoint":"workshop.mason","error":"bad json"}]}` {
		t.Errorf("JSON with an unreadable row is %s", b)
	}
}

// headings returns the unindented lines of a report: its house names.
func headings(report string) []string {
	var out []string
	for _, l := range strings.Split(strings.TrimRight(report, "\n"), "\n") {
		if !strings.HasPrefix(l, " ") {
			out = append(out, l)
		}
	}
	return out
}

// structure returns the house lines and the endpoint lines, in the order they
// print, with the field lines dropped.
func structure(report string) []string {
	var out []string
	for _, l := range strings.Split(strings.TrimRight(report, "\n"), "\n") {
		if strings.HasPrefix(l, "    ") {
			continue
		}
		out = append(out, l)
	}
	return out
}
