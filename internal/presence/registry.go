package presence

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// The registry report: every registration record the ledger holds, grouped by
// the house that owns it.
//
// IT IS PURE. It reads no file and no environment variable. The caller reads
// the ledger with ListAll and hands both lists in, so this package can be
// tested without a deployment, and so one reader of the ledger cannot disagree
// with another about what a row says.
//
// IT REPORTS NO HEALTH FACT. Whether a process is alive, whether a queue
// exists and how much mail waits are `loc status`'s answers, and they are
// derived from things this report does not look at. Two commands that answer
// the same question from different evidence will one day disagree, and the
// person reading them has no way to tell which one is wrong.

// registryLabel is the width every label is padded to, `registered:` being the
// longest of them.
const registryLabel = 12

// notGiven is what a field the agent never supplied prints.
//
// AN EMPTY VALUE AND AN ABSENT ONE MUST NOT LOOK ALIKE. A blank after the
// label reads as a display name of no characters, which is a different fact
// from an agent that declared no display name at all.
const notGiven = "(not given)"

// noHouse is the header for a ledger file whose name yields no house. It is
// placed after every real house by the sort below, and not by its spelling:
// the open bracket sorts BEFORE every letter, so plain string order would put
// it first.
const noHouse = "(no house)"

// Registry renders the whole record of every agent in rows, and names every
// row in unreadable that could not be parsed.
//
// house selects one house; the empty string means every house on this machine.
// An unreadable row belongs to the house its FILE NAME names, because the file
// name is the ledger's key and is readable when nothing inside the file is.
//
// A FILE NAME THAT YIELDS NO HOUSE STILL GETS A HEADER. A stray `notes.json`
// in the ledger directory is a name with no dot, so it has no house at all,
// and it used to print under an empty one — a blank line. It goes under
// noHouse instead, last, after every real house. With a house NAMED it is not
// shown: the caller asked about one house, and this row is in none.
//
// An empty registry is an ANSWER AND NOT A FAILURE. Nobody registered is what
// a fresh deployment looks like, and a caller that asked who is registered has
// been told.
func Registry(house string, rows []*Registration, unreadable []Unreadable) string {
	kept, keptBad := filterRegistry(house, rows, unreadable)
	if len(kept) == 0 && len(keptBad) == 0 {
		if house != "" {
			return fmt.Sprintf("(no agents registered in %s)\n", house)
		}
		return "(no agents registered)\n"
	}

	byHouse := map[string][]registryEntry{}
	for _, r := range kept {
		byHouse[headerFor(r.Endpoint)] = append(byHouse[headerFor(r.Endpoint)],
			registryEntry{endpoint: r.Endpoint, reg: r})
	}
	for _, u := range keptBad {
		byHouse[headerFor(u.Endpoint)] = append(byHouse[headerFor(u.Endpoint)],
			registryEntry{endpoint: u.Endpoint, err: u.Err})
	}
	houses := make([]string, 0, len(byHouse))
	for h := range byHouse {
		houses = append(houses, h)
	}
	// Real houses by name, and the houseless header last whatever it is
	// spelled. The leftovers belong at the end of the report.
	sort.Slice(houses, func(i, j int) bool {
		if (houses[i] == noHouse) != (houses[j] == noHouse) {
			return houses[j] == noHouse
		}
		return houses[i] < houses[j]
	})

	var b strings.Builder
	for _, h := range houses {
		entries := byHouse[h]
		// ListAll sorts each list by endpoint, and merging the two lists breaks
		// that order. This restores it, so the house reads in one sequence
		// rather than the readable rows followed by the broken ones.
		sort.Slice(entries, func(i, j int) bool { return entries[i].endpoint < entries[j].endpoint })
		fmt.Fprintf(&b, "%s\n", h)
		for _, e := range entries {
			fmt.Fprintf(&b, "  %s\n", e.endpoint)
			if e.reg == nil {
				line(&b, "unreadable:", fmt.Sprintf("%v", e.err))
				continue
			}
			line(&b, "agent:", fmt.Sprintf("%s %s", e.reg.Agent.Type, e.reg.Agent.Version))
			line(&b, "process:", processFacts(e.reg))
			line(&b, "registered:", e.reg.Registered)
			line(&b, "address:", orNotGiven(e.reg.Address))
			line(&b, "cwd:", orNotGiven(e.reg.Cwd))
			line(&b, "display:", displayFacts(e.reg.Display))
		}
	}
	return b.String()
}

// registryEntry is one agent in one house: either a record, or the reason its
// row could not be read. Exactly one of the two fields is set.
type registryEntry struct {
	endpoint string
	reg      *Registration
	err      error
}

// headerFor is the house line an endpoint prints under.
func headerFor(endpoint string) string {
	if h := Instance(endpoint); h != "" {
		return h
	}
	return noHouse
}

func line(b *strings.Builder, label, value string) {
	fmt.Fprintf(b, "    %-*s%s\n", registryLabel, label, value)
}

// processFacts is the recorded pair, not a question put to the operating
// system. Whether that pid is alive is `loc status`'s answer.
func processFacts(reg *Registration) string {
	return fmt.Sprintf("pid %d, started %s", reg.Process.PID, orNotGiven(reg.Process.Started))
}

// displayFacts is `<name> (<role>)`, or `<name>` when the agent declared no
// role. A Display the agent never sent is not given at all.
//
// THE NAME TAKES THE NOT-GIVEN RULE EVEN WHEN THERE IS A ROLE. `subscribe
// --role` with no `--display` stores a Display holding a role and an empty
// name, which is a real row and not a malformed one. Printing it as
// `(Records)` makes an absent name look like the whole display, and the rule
// this report keeps everywhere else is that an absent value and an empty one
// do not look alike.
func displayFacts(d *Display) string {
	if d == nil {
		return notGiven
	}
	if d.Role != "" {
		return orNotGiven(d.Name) + " (" + d.Role + ")"
	}
	return orNotGiven(d.Name)
}

func orNotGiven(s string) string {
	if s == "" {
		return notGiven
	}
	return s
}

// registryJSON is the envelope --json prints.
//
// EACH RECORD IS AS THIS BUILD READS IT: every field this build knows, with
// the stored values unchanged. A field written by a newer build is not
// carried, because the row is parsed into Registration and marshalled back
// out, and a key Registration has no home for is dropped in between. A
// consumer already reads this shape, and a second shape for the same records
// would be a second format for it to learn. The unreadable key appears only
// when a row could not be read, so its presence is itself the signal.
type registryJSON struct {
	Agents     []*Registration  `json:"agents"`
	Unreadable []unreadableJSON `json:"unreadable,omitempty"`
}

type unreadableJSON struct {
	Endpoint string `json:"endpoint"`
	Error    string `json:"error"`
}

// RegistryJSON renders the same selection as Registry, as one JSON object.
func RegistryJSON(house string, rows []*Registration, unreadable []Unreadable) ([]byte, error) {
	kept, keptBad := filterRegistry(house, rows, unreadable)
	out := registryJSON{Agents: kept}
	if out.Agents == nil {
		// An empty registry is `"agents":[]` and never `"agents":null`: a
		// consumer that iterates the key should not have to test for absence.
		out.Agents = []*Registration{}
	}
	for _, u := range keptBad {
		out.Unreadable = append(out.Unreadable, unreadableJSON{Endpoint: u.Endpoint, Error: fmt.Sprintf("%v", u.Err)})
	}
	return json.Marshal(out)
}

// filterRegistry keeps the rows of one house, or every row when house is
// empty. It preserves the order it was given.
func filterRegistry(house string, rows []*Registration, unreadable []Unreadable) ([]*Registration, []Unreadable) {
	if house == "" {
		return rows, unreadable
	}
	var kept []*Registration
	for _, r := range rows {
		if Instance(r.Endpoint) == house {
			kept = append(kept, r)
		}
	}
	var keptBad []Unreadable
	for _, u := range unreadable {
		if Instance(u.Endpoint) == house {
			keptBad = append(keptBad, u)
		}
	}
	return kept, keptBad
}
