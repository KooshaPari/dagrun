package plan

import "testing"

func parsePlan(t *testing.T, src string) *Plan {
	t.Helper()
	p, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return p
}

func codes(issues []Issue) []string {
	out := make([]string, 0, len(issues))
	for _, is := range issues {
		out = append(out, is.Code)
	}
	return out
}

func hasCode(issues []Issue, code string) bool {
	for _, is := range issues {
		if is.Code == code {
			return true
		}
	}
	return false
}

// Two independent nodes writing the same artifact race by construction.
func TestOverlapsRejectsParallelSameArtifact(t *testing.T) {
	p := parsePlan(t, `
version: 1
nodes:
  - {id: "1", kind: IMPL, artifacts: [{path: artifacts/out.md}]}
  - {id: "2", kind: IMPL, artifacts: [{path: artifacts/out.md}]}
`)
	got := p.Overlaps()
	if !hasCode(got, "artifact_collision") {
		t.Fatalf("expected artifact_collision, got %v", codes(got))
	}
	if got[0].Node != "1" {
		t.Errorf("issue should name the lower id first, got %q", got[0].Node)
	}
	// The plan must be rejected by Validate too.
	if !hasCode(p.Validate(), "artifact_collision") {
		t.Fatal("Validate did not surface the collision")
	}
}

// A dependency edge makes the same artifact a legitimate hand-off, not a race.
func TestOverlapsAllowsSequentialSameArtifact(t *testing.T) {
	p := parsePlan(t, `
version: 1
nodes:
  - {id: "1", kind: IMPL, artifacts: [{path: artifacts/out.md}]}
  - {id: "2", kind: IMPL, deps: ["1"], artifacts: [{path: artifacts/out.md}]}
  - {id: "3", kind: IMPL, deps: ["2"], artifacts: [{path: artifacts/out.md}]}
`)
	if got := p.Overlaps(); len(got) != 0 {
		t.Fatalf("sequential hand-off must be allowed, got %v", codes(got))
	}
	if got := p.Validate(); len(got) != 0 {
		t.Fatalf("clean plan rejected: %v", got)
	}
}

func TestOverlapsDetectsGlobScopeOverlap(t *testing.T) {
	cases := []struct {
		name string
		a    string
		b    string
		want bool
	}{
		{"identical", "internal/store/*.go", "internal/store/*.go", true},
		{"glob matches literal", "internal/store/*.go", "internal/store/lease.go", true},
		{"literal matches glob", "internal/store/lease.go", "internal/store/*.go", true},
		{"sibling leaves", "internal/store/*.go", "internal/plan/*.go", false},
		{"different subtrees", "internal/plan/*.go", "cmd_plan.go", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := `
version: 1
nodes:
  - {id: "1", kind: IMPL, artifacts: [{path: a1}], file_scope: ["` + c.a + `"]}
  - {id: "2", kind: IMPL, artifacts: [{path: a2}], file_scope: ["` + c.b + `"]}
`
			got := hasCode(parsePlan(t, src).Overlaps(), "scope_overlap")
			if got != c.want {
				t.Fatalf("scope_overlap = %v, want %v", got, c.want)
			}
		})
	}
}

func TestOverlapsIgnoresUnrelatedScopes(t *testing.T) {
	p := parsePlan(t, `
version: 1
nodes:
  - {id: "1", kind: IMPL, artifacts: [{path: a}], file_scope: ["internal/plan/*.go"]}
  - {id: "2", kind: IMPL, artifacts: [{path: b}], file_scope: ["internal/store/*.go"]}
  - {id: "3", kind: IMPL, deps: ["1"], artifacts: [{path: c}], file_scope: ["internal/plan/*.go"]}
`)
	if got := p.Overlaps(); len(got) != 0 {
		t.Fatalf("expected no overlap, got %v", got)
	}
	if got := p.Validate(); len(got) != 0 {
		t.Fatalf("clean plan rejected: %v", got)
	}
}

// Transitive ancestry, not just a direct dep, makes nodes sequential.
func TestOverlapsTransitiveAncestryIsSequential(t *testing.T) {
	p := parsePlan(t, `
version: 1
nodes:
  - {id: "1", kind: IMPL, artifacts: [{path: a}], file_scope: ["x/*.go"]}
  - {id: "2", kind: IMPL, deps: ["1"], artifacts: [{path: b}]}
  - {id: "3", kind: IMPL, deps: ["2"], artifacts: [{path: c}], file_scope: ["x/*.go"]}
`)
	if got := p.Overlaps(); len(got) != 0 {
		t.Fatalf("1 and 3 are sequential through 2, got %v", codes(got))
	}
}

// Optional nodes and fan_out templates are exempt: the template never runs and
// an optional node carries no contract.
func TestOverlapsSkipsOptionalAndFanOutTemplates(t *testing.T) {
	p := parsePlan(t, `
version: 1
nodes:
  - {id: "1", kind: IMPL, artifacts: [{path: a}], file_scope: ["x/*.go"]}
  - {id: "2", kind: IMPL, optional: true, artifacts: [{path: a}], file_scope: ["x/*.go"]}
  - {id: "3", kind: IMPL, fan_out: "1", artifacts: [{path: a}], file_scope: ["x/*.go"]}
`)
	if got := p.Overlaps(); len(got) != 0 {
		t.Fatalf("optional and template nodes must be exempt, got %v", codes(got))
	}
}

func TestOverlapsIsDeterministic(t *testing.T) {
	p := parsePlan(t, `
version: 1
nodes:
  - {id: "1.10", kind: IMPL, artifacts: [{path: same}]}
  - {id: "1.2", kind: IMPL, artifacts: [{path: same}]}
  - {id: "2.1", kind: IMPL, artifacts: [{path: same}]}
`)
	first := p.Overlaps()
	if len(first) != 3 {
		t.Fatalf("want 3 pair collisions, got %v", codes(first))
	}
	for i := 0; i < 5; i++ {
		again := p.Overlaps()
		if len(again) != len(first) {
			t.Fatalf("run %d: %d issues, want %d", i, len(again), len(first))
		}
		for j := range again {
			if again[j] != first[j] {
				t.Fatalf("run %d issue %d = %v, want %v", i, j, again[j], first[j])
			}
		}
	}
	// natural id order: the lower id of each pair sorts first
	wantOrder := []string{"1.2", "1.2", "1.10"}
	for i, want := range wantOrder {
		if first[i].Node != want {
			t.Fatalf("issue %d names %q, want %q: %v", i, first[i].Node, want, first)
		}
	}
}
