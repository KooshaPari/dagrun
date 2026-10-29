package plan

import (
	"strings"
	"testing"
)

// TestTemplatePlans is the acceptance test for the shipped plan templates in
// templates/. Each template is a real plan an agent can run, so each one must
// pass Validate with zero issues, carry real work (>= 4 nodes), and leave every
// node verifiable: an artifact or a check, never neither.
func TestTemplatePlans(t *testing.T) {
	all := Templates()
	if len(all) == 0 {
		t.Fatal("no templates embedded")
	}

	want := map[string]bool{
		"greenfield":      false,
		"bugfix":          false,
		"migration":       false,
		"audit":           false,
		"next-usable-cvp": false,
		"lint-sweep":      false,
	}

	for _, tmpl := range all {
		if _, ok := want[tmpl.Name]; ok {
			want[tmpl.Name] = true
		}

		t.Run(tmpl.Name, func(t *testing.T) {
			raw, err := Template(tmpl.Name)
			if err != nil {
				t.Fatalf("read %s: %v", tmpl.Name, err)
			}
			p, err := Parse(raw)
			if err != nil {
				t.Fatalf("parse %s: %v", tmpl.Name, err)
			}
			for _, is := range p.Validate() {
				t.Errorf("%s: %s", tmpl.Name, is)
			}
			if len(p.Nodes) < 4 {
				t.Errorf("%s: %d nodes, want at least 4", tmpl.Name, len(p.Nodes))
			}
			for _, n := range p.Nodes {
				if n == nil {
					continue
				}
				if len(n.Artifacts) == 0 && n.Check == "" && n.FanOut == "" && !n.Optional {
					t.Errorf("%s: node %s declares neither artifacts nor a check", tmpl.Name, n.ID)
				}
			}
		})
	}

	for name, seen := range want {
		if !seen {
			t.Errorf("missing template %s", name)
		}
	}
}

// TestTemplatesSortedAndDescribed pins the shape of the listing: sorted by name
// and every entry explained, because the list is what a person reads to choose.
func TestTemplatesSortedAndDescribed(t *testing.T) {
	all := Templates()
	if len(all) < 6 {
		t.Fatalf("got %d templates, want at least 6", len(all))
	}
	for i, tmpl := range all {
		if tmpl.Name == "" {
			t.Errorf("template %d has an empty name", i)
		}
		if strings.TrimSpace(tmpl.Description) == "" {
			t.Errorf("template %q has no description", tmpl.Name)
		}
		if i > 0 && all[i-1].Name > tmpl.Name {
			t.Errorf("templates not sorted: %q before %q", all[i-1].Name, tmpl.Name)
		}
	}
}

// TestTemplateUnknownName checks the error path names the alternatives, so a
// typo is answered rather than just rejected.
func TestTemplateUnknownName(t *testing.T) {
	for _, name := range []string{"", "nope", "../plan", "greenfield/../../main"} {
		raw, err := Template(name)
		if err == nil {
			t.Errorf("Template(%q) returned %d bytes, want error", name, len(raw))
			continue
		}
		if !strings.Contains(err.Error(), "template") {
			t.Errorf("Template(%q) error %q does not mention template", name, err)
		}
	}

	if _, err := Template("greenfield.yaml"); err != nil {
		t.Errorf("Template(\"greenfield.yaml\") should tolerate the .yaml suffix: %v", err)
	}
}
