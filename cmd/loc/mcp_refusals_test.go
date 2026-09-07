package main

// What `mcp` refuses, and how.
//
// A server the runtime cannot start must FAIL, loudly and immediately: the
// runtime then shows it as failed, which is the truth, and the alternative —
// a server that starts and quietly serves nothing — is a seat that looks
// present and is not. None of these cases reaches a medium.

import "testing"

func TestMCP_TrailingArgumentsAreAUsageError(t *testing.T) {
	newDeployment(t, "ada")
	code, out, errOut := exec("mcp", "--stdio")
	assertResult(t, code, out, errOut, 1, usage, "")
}

// A BARE IDENTITY IS REFUSED BY NAME. A seat is exactly the thing that gets
// registered, and the presence model bars bare endpoints — so an unqualified
// name is not a seat this server could hold, and it is told which form it
// needed rather than failing later and elsewhere.
func TestMCP_ABareIdentityIsRefusedByName(t *testing.T) {
	newDeployment(t, "ada")
	checkOnStderr(t, "endpoint must match <instance>.<agent>", "mcp")
}

func TestMCP_NoIdentityIsRefused(t *testing.T) {
	newDeployment(t, "ada")
	t.Setenv("LOC_IDENTITY", "")
	checkOnStderr(t, "cannot determine sender identity", "mcp")
}

// A medium that carries messages but cannot say when one arrives is named in
// the refusal: "this provider cannot" and "you typed it wrong" are different
// answers, and only one of them is fixed by reading the usage.
func TestMCP_AProviderThatCannotRingIsNamedInTheRefusal(t *testing.T) {
	d := newDeployment(t, "ada")
	t.Setenv("LOC_IDENTITY", "workshop.scribe")
	stampVersion(t, "1.4.2")
	checkOnStderr(t, "cannot tell a seat when its mail arrives", "mcp")
	if d.spy.closed != 1 {
		t.Errorf("the provider was opened and not closed (%d closes)", d.spy.closed)
	}
}
