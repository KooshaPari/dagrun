package plan

import (
	"strings"
	"testing"
)

// templateAdvisories records the advisory findings ("warn:" codes) a shipped
// template is allowed to produce, keyed by "template/code". Anything missing
// from this map fails the test, so a template that drifts into a new warning
// fails the build instead of quietly degrading.
//
// The value is the reason the warning is inherent, not a bug to be fixed.
var templateAdvisories = map[string]string{
	"lint-sweep/warn:unreachable": "node 1.3 is the fan_out template for the discovered work set: " +
		"it never executes itself, so nothing can depend on it directly, and the reduce (1.4) " +
		"reaches the discover node (1.2) through fan_in rather than a dep. Giving 1.4 a dep on 1.3 " +
		"would be wrong as well: the runner expands 1.3 into 1.3/<slug> children at run time, which " +
		"no static plan can name.",
}

// TestTemplatePlans is the acceptance test for the shipped plan templates in
// templates/. Each template is a real plan an agent can run, so each one must
// pass BOTH linters with zero errors: Validate (structural: cycles, unknown
// deps, bad paths, overlaps) and SemanticIssues+ScopeIssues (a SPEC with no
// TEST, an IMPL with no SPEC/PLAN ancestor, overlapping parallel scope, a soft
// gate). Advisory findings are allowed only when listed in templateAdvisories,
// and are always printed so a new one is visible in `make templates`.
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

	usedAdvisory := map[string]bool{}

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

			checks := []struct {
				linter string
				issues []Issue
			}{
				{"validate", p.Validate()},
				{"semantic", p.SemanticIssues()},
				{"scope", p.ScopeIssues()},
			}
			for _, c := range checks {
				for _, is := range c.issues {
					if strings.HasPrefix(is.Code, "warn:") {
						key := tmpl.Name + "/" + is.Code
						if _, ok := templateAdvisories[key]; !ok {
							t.Errorf("%s: %s: unlisted advisory %s (node %s): %s",
								tmpl.Name, c.linter, is.Code, is.Node, is.Detail)
							continue
						}
						usedAdvisory[key] = true
						t.Logf("%s: %s: ADVISORY %s (node %s, allowed): %s",
							tmpl.Name, c.linter, is.Code, is.Node, is.Detail)
						continue
					}
					t.Errorf("%s: %s: %s (node %s): %s", tmpl.Name, c.linter, is.Code, is.Node, is.Detail)
				}
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
	// An allowlist entry that no template produces any more is stale, and a
	// stale exception is how a real warning gets ignored later.
	for key := range templateAdvisories {
		if !usedAdvisory[key] {
			t.Errorf("stale advisory allowlist entry %q: no template produces it; delete it", key)
		}
	}
}

// kindArtifactWords is the word set each node kind's artifact must declare,
// mirroring the contracts in internal/verify/kinds.go. The templates carry the
// same words in their must_contain lists, so an agent that fills a template
// produces an artifact the kind check accepts instead of discovering the gap at
// verification time.
var kindArtifactWords = map[string][]string{
	"INTENT":   {"Acceptance"},
	"SPEC":     {"Requirement", "Scenario"},
	"RESEARCH": {"Decision", "Alternatives"},
	"SOTA":     {"Verdict"},
}

// TestTemplateArtifactContractsMatchKinds keeps the templates and the kind
// contracts in step. It is the static half of the check: with no artifacts on
// disk nothing can be read, but the declared predicates are what the agent is
// told to satisfy, so they must contain everything the kind contract requires.
func TestTemplateArtifactContractsMatchKinds(t *testing.T) {
	for _, tmpl := range Templates() {
		raw, err := Template(tmpl.Name)
		if err != nil {
			t.Fatalf("read %s: %v", tmpl.Name, err)
		}
		p, err := Parse(raw)
		if err != nil {
			t.Fatalf("parse %s: %v", tmpl.Name, err)
		}

		for _, n := range p.Sorted() {
			if n == nil {
				continue
			}

			if words, ok := kindArtifactWords[n.Kind]; ok {
				var declared []string
				var patterns []string
				for _, a := range n.Artifacts {
					declared = append(declared, a.MustContain...)
					if a.MustMatch != "" {
						patterns = append(patterns, a.MustMatch)
					}
				}
				haystack := strings.ToLower(strings.Join(declared, "\n"))
				for _, w := range words {
					if !strings.Contains(haystack, strings.ToLower(w)) {
						t.Errorf("%s: %s node %s does not require %q in must_contain; "+
							"the %s kind contract rejects an artifact without it",
							tmpl.Name, n.Kind, n.ID, w, n.Kind)
					}
				}
				if n.Kind == "INTENT" && len(patterns) == 0 && !strings.Contains(haystack, "- ") {
					t.Errorf("%s: INTENT node %s does not require acceptance bullets; "+
						"the INTENT contract needs at least 3 \"- \" bullets", tmpl.Name, n.ID)
				}
			}

			switch n.Kind {
			case "TEST":
				hasTestFile := false
				for _, a := range n.Artifacts {
					if strings.Contains(strings.ToLower(a.Path), "test") {
						hasTestFile = true
						break
					}
				}
				if !hasTestFile {
					t.Errorf("%s: TEST node %s declares no artifact path containing \"test\"; "+
						"the TEST contract rejects it", tmpl.Name, n.ID)
				}
			case "RESEARCH":
				if !strings.Contains(n.Check, "http") {
					t.Errorf("%s: RESEARCH node %s has no check counting http sources; "+
						"the RESEARCH contract needs 2+ distinct URLs", tmpl.Name, n.ID)
				}
			case "DOC":
				// The DOC contract is "no broken relative links", which cannot be
				// expressed as a predicate. Requiring a positive predicate at least
				// keeps the artifact from being an empty placeholder.
				declared := false
				for _, a := range n.Artifacts {
					if len(a.MustContain) > 0 || a.MustMatch != "" {
						declared = true
					}
				}
				if !declared {
					t.Errorf("%s: DOC node %s declares no must_contain/must_match", tmpl.Name, n.ID)
				}
			}
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
