package plan

import (
	"strings"
	"testing"
)

func TestDraftProducesAValidPlan(t *testing.T) {
	p, err := Draft("Make the CLI installable and document it. Run `go test ./...` before shipping.", "demo")
	if err != nil {
		t.Fatal(err)
	}
	if issues := p.Validate(); len(issues) != 0 {
		t.Fatalf("drafted plan has issues: %v", issues)
	}
	kinds := map[string]bool{}
	for _, n := range p.Nodes {
		kinds[n.Kind] = true
	}
	for _, want := range []string{"INTENT", "SPEC", "TEST", "IMPL", "GATE"} {
		if !kinds[want] {
			t.Errorf("drafted plan has no %s node", want)
		}
	}
	ids := map[string]bool{}
	for _, n := range p.Nodes {
		if ids[n.ID] {
			t.Errorf("duplicate id %s", n.ID)
		}
		ids[n.ID] = true
		if !strings.Contains(n.ID, ".") {
			t.Errorf("id %q is not hierarchical", n.ID)
		}
	}
}

func TestDraftRoundTripsThroughYAML(t *testing.T) {
	intent := "Ship the thing.\nCheck with `go build ./...`."
	raw, err := DraftYAML(intent, "rt")
	if err != nil {
		t.Fatal(err)
	}
	p, err := Parse(raw)
	if err != nil {
		t.Fatalf("emitted YAML does not parse: %v\n%s", err, raw)
	}
	if issues := p.Validate(); len(issues) != 0 {
		t.Fatalf("emitted YAML does not validate: %v\n%s", issues, raw)
	}
	raw2, err := DraftYAML(p.Intent, "rt")
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(raw2) {
		t.Error("draft is not idempotent: a second pass differs")
	}
}

func TestDraftTakesTheGateCommandFromTheIntent(t *testing.T) {
	cases := []struct {
		intent string
		want   string
	}{
		{"Run `go test ./...` before shipping.", "go test ./..."},
		{"build the thing with `make release`", "make release"},
		{"the `store` package must stay small", "true"},
		{"no command here at all", "true"},
		{"run cargo test --workspace", "run cargo test --workspace"},
	}
	for _, c := range cases {
		p, err := Draft(c.intent, "x")
		if err != nil {
			t.Fatalf("%q: %v", c.intent, err)
		}
		var gate *Node
		for _, n := range p.Nodes {
			if n.Kind == "GATE" {
				gate = n
			}
		}
		if gate == nil {
			t.Fatalf("%q: no GATE node", c.intent)
		}
		if gate.Check != c.want {
			t.Errorf("%q: gate check = %q, want %q", c.intent, gate.Check, c.want)
		}
	}
}

func TestDraftRejectsEmptyIntent(t *testing.T) {
	if _, err := Draft("   ", "x"); err == nil {
		t.Fatal("expected an error for an empty intent")
	}
}
