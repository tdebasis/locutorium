package loc

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestEndpointsAndMembership(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOC_HOME", home)

	if EndpointExists("ada") {
		t.Error("an absent registry cannot contain anything")
	}
	if got := Endpoints(); got != nil {
		t.Errorf("an absent registry is empty, got %v", got)
	}

	if err := os.WriteFile(filepath.Join(home, "endpoints"), []byte("ada\nbob\n\ncarol\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if want := []string{"ada", "bob", "carol"}; !reflect.DeepEqual(Endpoints(), want) {
		t.Errorf("Endpoints() = %v, want %v", Endpoints(), want)
	}
	for _, name := range []string{"ada", "bob", "carol"} {
		if !EndpointExists(name) {
			t.Errorf("%s is in the registry but was not found", name)
		}
	}
	// Whole line, fixed string: a registry entry is a name, not a pattern and
	// not a prefix.
	for _, name := range []string{"ad", "adax", "a.a", "mallory", ""} {
		if EndpointExists(name) {
			t.Errorf("%q is not in the registry but was found", name)
		}
	}
}
