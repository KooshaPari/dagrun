package plan

import (
	"strings"
	"testing"
)

// TestPromptStatesTheKindContract proves the node prompt tells the agent what
// verification will enforce.
//
// This is a regression test for a real live-run failure: the agent satisfied
// every DECLARED artifact requirement (the file existed, was big enough, and
// contained the required string) and still failed verification with
// "intent_thin_acceptance", because the INTENT kind's own contract — an
// Acceptance Criteria heading plus at least three bullets — was enforced but
// never stated. A contract the agent cannot see is a contract it cannot meet.
func TestPromptStatesTheKindContract(t *testing.T) {
	p := &Plan{
		Version: 1,
		Run:     "k",
		Nodes: []*Node{
			{ID: "1.1", Kind: "INTENT", Title: "t", Artifacts: []Artifact{{Path: "artifacts/A.md"}}},
		},
	}
	idx := map[string]*Node{"1.1": p.Nodes[0]}
	got := Prompt(p, p.Nodes[0], idx)

	if !strings.Contains(got, "Acceptance Criteria") {
		t.Fatalf("prompt does not mention the Acceptance Criteria requirement:\n%s", got)
	}
	if !strings.Contains(got, "at least 3 bullets") {
		t.Fatalf("prompt does not state the bullet-count requirement:\n%s", got)
	}
	if !strings.Contains(got, "Contract for this node kind") {
		t.Fatalf("prompt has no kind-contract section:\n%s", got)
	}
}

// TestPromptOmitsKindContractForKindsWithoutOne proves the section only
// appears when the kind actually has a contract. A node of kind IMPL must not
// be told about acceptance criteria.
func TestPromptOmitsKindContractForKindsWithoutOne(t *testing.T) {
	p := &Plan{
		Version: 1,
		Run:     "k",
		Nodes: []*Node{
			{ID: "2.1", Kind: "IMPL", Title: "t", Artifacts: []Artifact{{Path: "artifacts/a.go"}}},
		},
	}
	idx := map[string]*Node{"2.1": p.Nodes[0]}
	got := Prompt(p, p.Nodes[0], idx)

	if strings.Contains(got, "Contract for this node kind") {
		t.Fatalf("prompt has a kind-contract section for a kind without one:\n%s", got)
	}
}

// TestKindRequirementsCoverEveryCheckedKind proves every kind the verifier
// checks has a stated requirement list. A kind the verifier enforces but the
// prompt never mentions is the bug this whole file exists to prevent.
//
// The verifier's checked kinds live in verify/kinds.go; this list is the
// prompt-side mirror of it.
func TestKindRequirementsCoverEveryCheckedKind(t *testing.T) {
	checked := []string{"SPEC", "TEST", "RESEARCH", "SOTA", "INTENT", "UI", "DOC"}
	for _, k := range checked {
		if len(KindRequirements(k)) == 0 {
			t.Errorf("kind %q is checked by the verifier but has no stated requirements", k)
		}
	}
	// Kinds with no extra contract must stay silent.
	for _, k := range []string{"IMPL", "CODE", "GATE", "PLAN", "OTHER", ""} {
		if len(KindRequirements(k)) != 0 {
			t.Errorf("kind %q has no verifier contract but states %d requirements", k, len(KindRequirements(k)))
		}
	}
}
