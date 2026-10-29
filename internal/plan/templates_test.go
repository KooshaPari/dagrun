package plan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTemplatePlans is the acceptance test for the shipped plan templates in
// examples/templates. Each template is a real plan an agent can run, so each
// one must pass Validate with zero issues, carry real work (>= 4 nodes), and
// leave every node verifiable: an artifact or a check, never neither.
func TestTemplatePlans(t *testing.T) {
	dir := filepath.Join("..", "..", "examples", "templates")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read templates dir: %v", err)
	}

	want := map[string]bool{
		"greenfield.yaml":      false,
		"bugfix.yaml":          false,
		"migration.yaml":       false,
		"audit.yaml":           false,
		"next-usable-cvp.yaml": false,
		"lint-sweep.yaml":      false,
	}

	found := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		name := e.Name()
		found++
		if _, ok := want[name]; ok {
			want[name] = true
		}

		t.Run(strings.TrimSuffix(name, ".yaml"), func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatalf("read %s: %v", name, err)
			}
			p, err := Parse(raw)
			if err != nil {
				t.Fatalf("parse %s: %v", name, err)
			}
			for _, is := range p.Validate() {
				t.Errorf("%s: %s", name, is)
			}
			if len(p.Nodes) < 4 {
				t.Errorf("%s: %d nodes, want at least 4", name, len(p.Nodes))
			}
			for _, n := range p.Nodes {
				if n == nil {
					continue
				}
				if len(n.Artifacts) == 0 && n.Check == "" && n.FanOut == "" && !n.Optional {
					t.Errorf("%s: node %s declares neither artifacts nor a check", name, n.ID)
				}
			}
		})
	}

	if found == 0 {
		t.Fatalf("no *.yaml templates found under %s", dir)
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("missing template %s", name)
		}
	}
}
