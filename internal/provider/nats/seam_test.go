package nats

// ONE SEAM FOR EVERY DIAL, enforced rather than remembered.
//
// guard.go states the invariant in its own header: "Every caller in the module
// and the daemon uses it; a new natsgo.Connect anywhere else is a defect."
// Nothing tested that sentence, and three sites were already outside it when
// this was written. A rule with no step behind it is followed while somebody
// remembers and stops when nobody does.
//
// This walks the module and reports every natsgo.Connect outside this package.
// Three sites are allowed BY NAME, each with the reason, so an exemption is
// visible rather than absent. The list is checked in both directions: an entry
// that no longer matches anything fails too, so it cannot rot into a licence
// nobody reads.
//
// WHY THE THREE ARE NOT REROUTED THROUGH Dial, which was the other fix offered.
// All three are independent observers, and Dial reads the deployment's config.
// adminAt serves a case that REWRITES the config mid-test and then asks a named
// broker what it actually holds; assertTopicsStream "asks the broker itself,
// not the code that made the stream"; loctest opens the harness's own admin
// connection to a scratch server it booted. Routing an observer through the
// code it observes is what read_verbs_test.go's post helper already refuses:
// "so nothing under test is used to set up what is under test."
//
// The seam is for the PRODUCT's dials. This test is what keeps the next one
// from drifting out of it.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const natsImportPath = "github.com/nats-io/nats.go"

// dialsOutsideTheSeam is the allowlist. The key is a module-relative path with
// forward slashes. The value is why that file may dial directly, and it is
// written for a reader who finds a fourth site and has to decide.
var dialsOutsideTheSeam = map[string]string{
	"cmd/loc/no_deployment_test.go": "adminAt is an independent observer. Its case rewrites the " +
		"deployment's config and then asks a NAMED broker what it holds, so a dial that read that " +
		"config would answer about the wrong one and the case would prove nothing.",
	"cmd/loc/daemon_test.go": "assertTopicsStream asks the broker itself, not the code that made " +
		"the stream. Its own comment says so.",
	"internal/loctest/loctest.go": "the harness's admin connection, to a scratch server loctest " +
		"booted on a port the kernel picked. Dial requires a deployment config file, and the " +
		"harness boots homes that deliberately have none.",
}

type dialSite struct {
	path string // module-relative, forward slashes
	line int
}

// findDialsOutsideTheSeam walks root and returns every call of Connect on the
// nats.go package, except those inside this package's own directory.
//
// It reads each file's IMPORT SPEC rather than assuming the alias. Every file
// in this module spells it natsgo today; a file that spells it differently
// tomorrow is still seen, which is the point of a guard.
func findDialsOutsideTheSeam(root string) ([]dialSite, error) {
	var found []dialSite
	seam := filepath.Join("internal", "provider", "nats")

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if d.IsDir() {
			// The seam's own package is where Connect belongs. Skip the
			// version control directory and any vendored tree for speed and
			// because neither is this module's source.
			if rel == seam || d.Name() == ".git" || d.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}

		fset := token.NewFileSet()
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}

		alias := ""
		for _, imp := range file.Imports {
			p, uErr := strconv.Unquote(imp.Path.Value)
			if uErr != nil || p != natsImportPath {
				continue
			}
			if imp.Name != nil {
				alias = imp.Name.Name
			} else {
				alias = "nats" // the package's own name when it is not aliased
			}
		}
		if alias == "" || alias == "_" {
			return nil
		}

		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Connect" {
				return true
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok || ident.Name != alias {
				return true
			}
			found = append(found, dialSite{
				path: filepath.ToSlash(rel),
				line: fset.Position(call.Pos()).Line,
			})
			return true
		})
		return nil
	})
	return found, err
}

// moduleRoot walks up from the working directory for go.mod.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s", dir)
		}
		dir = parent
	}
}

// TestEveryDialGoesThroughTheSeam fails on a dial this file does not name.
func TestEveryDialGoesThroughTheSeam(t *testing.T) {
	root := moduleRoot(t)
	sites, err := findDialsOutsideTheSeam(root)
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}

	matched := map[string]bool{}
	for _, s := range sites {
		if _, allowed := dialsOutsideTheSeam[s.path]; allowed {
			matched[s.path] = true
			continue
		}
		t.Errorf("%s:%d dials the broker directly, outside %s.\n"+
			"Every dial goes through Dial in this package, which refuses the default broker "+
			"under go test. A helper that dials around the seam is one edit from the incident "+
			"the guard exists for.\n"+
			"Route it through Dial, or add it to dialsOutsideTheSeam with the reason it must "+
			"not be routed.",
			s.path, s.line, natsImportPath)
	}

	// THE LIST IS CHECKED IN BOTH DIRECTIONS. An entry whose file no longer
	// dials directly is a licence nobody is using, and a stale exemption reads
	// as permission the next time somebody edits that file.
	for path := range dialsOutsideTheSeam {
		if !matched[path] {
			t.Errorf("dialsOutsideTheSeam names %s, but no direct dial was found there. "+
				"Remove the entry: an exemption that matches nothing is a licence waiting "+
				"for the next edit.", path)
		}
	}
}

// TestTheSeamWalkerFindsAPlantedDial is the fire control for the walker above.
//
// A test that reports a clean tree proves nothing until the instrument has been
// shown to grip. The plant sits in a real directory with a real import, because
// the walker parses Go rather than matching text.
func TestTheSeamWalkerFindsAPlantedDial(t *testing.T) {
	for _, arm := range []struct {
		name  string
		body  string
		want  int
		alias string
	}{
		{
			name: "an aliased import is found",
			body: "package plant\n\nimport natsgo \"" + natsImportPath + "\"\n\n" +
				"func dial() { _, _ = natsgo.Connect(\"nats://127.0.0.1:4222\") }\n",
			want: 1,
		},
		{
			name: "an UNALIASED import is found, which the fixed-alias form would miss",
			body: "package plant\n\nimport \"" + natsImportPath + "\"\n\n" +
				"func dial() { _, _ = nats.Connect(\"nats://127.0.0.1:4222\") }\n",
			want: 1,
		},
		{
			name: "NEGATIVE CONTROL: another package's Connect is not a nats dial",
			body: "package plant\n\nimport natsgo \"" + natsImportPath + "\"\n\n" +
				"type other struct{}\n\nfunc (other) Connect(string) {}\n\n" +
				"func dial() { var o other; o.Connect(\"x\"); _ = natsgo.Timeout }\n",
			want: 0,
		},
		{
			name: "NEGATIVE CONTROL: a file that never imports nats.go",
			body: "package plant\n\nfunc dial() {}\n",
			want: 0,
		},
	} {
		t.Run(arm.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "cmd", "plant")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(filepath.Join(dir, "plant.go"), []byte(arm.body), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
			sites, err := findDialsOutsideTheSeam(root)
			if err != nil {
				t.Fatalf("walk: %v", err)
			}
			if len(sites) != arm.want {
				t.Fatalf("found %d dials, want %d: %+v", len(sites), arm.want, sites)
			}
		})
	}
}

// TestTheSeamsOwnPackageIsNotWalked pins the exclusion, so a refactor that
// moves the seam cannot make this test pass by walking nothing.
func TestTheSeamsOwnPackageIsNotWalked(t *testing.T) {
	root := moduleRoot(t)
	sites, err := findDialsOutsideTheSeam(root)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	for _, s := range sites {
		if strings.HasPrefix(s.path, "internal/provider/nats/") {
			t.Errorf("%s:%d is inside the seam and should have been skipped", s.path, s.line)
		}
	}
	// AND THE WALKER REACHED SOMETHING. A skip that swallowed the whole tree
	// would satisfy the loop above by finding nothing at all.
	if len(sites) == 0 {
		t.Fatal("the walker found no direct dial anywhere, not even the three this file names; " +
			"it did not reach the tree")
	}
}
