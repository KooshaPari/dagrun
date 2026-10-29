package report

import (
	"strings"
	"testing"
	"time"

	"github.com/KooshaPari/dagrun/internal/plan"
	"github.com/KooshaPari/dagrun/internal/store"
	"github.com/KooshaPari/dagrun/internal/trace"
)

const samplePlan = `
version: 1
run: sample
intent: prove the report
nodes:
  - id: "1.1"
    kind: INTENT
    title: intent
    artifacts: [{path: artifacts/INTENT.md, min_bytes: 20}]
  - id: "1.2"
    kind: IMPL
    title: implement
    deps: ["1.1"]
    artifacts: [{path: artifacts/code.go}]
  - id: "1.3"
    kind: GATE
    title: gate
    deps: ["1.2"]
    check: "true"
`

func TestCritiqueFindsTheGaps(t *testing.T) {
	p, err := plan.Parse([]byte(samplePlan))
	if err != nil {
		t.Fatal(err)
	}
	c := CritiquePlan(p)
	out := RenderCritique(c)

	for _, want := range []string{"1.1", "1.2", "1.3", "Contract gaps", "Missing gates"} {
		if !strings.Contains(out, want) {
			t.Errorf("critique missing %q:\n%s", want, out)
		}
	}
	// the IMPL has no SPEC ancestor
	if len(c.Risks) == 0 {
		t.Error("expected a risk for the IMPL without a SPEC ancestor")
	}
	// the soft gate must be named
	if len(c.MissingGates) == 0 {
		t.Error("expected the soft gate to be reported")
	}
	// concrete edits must be numbered
	if len(c.Edits) == 0 || !strings.Contains(out, "1. fix:") {
		t.Errorf("expected numbered concrete edits:\n%s", out)
	}
}

func TestCritiqueOnACleanPlanSaysSo(t *testing.T) {
	// A clean plan has a SPEC ancestor for its IMPL, a real gate, and every
	// artifact bounded. The IMPL-without-SPEC risk is a genuine finding, so a
	// plan that omits the SPEC is not clean by design.
	yaml := `
version: 1
run: clean
nodes:
  - id: "1.1"
    kind: SPEC
    artifacts: [{path: spec.md, min_bytes: 3}]
  - id: "1.2"
    kind: TEST
    deps: ["1.1"]
    artifacts: [{path: a_test.go, min_bytes: 3}]
  - id: "1.3"
    kind: IMPL
    deps: ["1.2"]
    artifacts: [{path: a.txt, min_bytes: 3}]
  - id: "1.4"
    kind: GATE
    deps: ["1.3"]
    check: "test -f a.txt"
`
	p, err := plan.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	c := CritiquePlan(p)
	out := RenderCritique(c)
	if len(c.Gaps) != 0 {
		t.Errorf("unexpected gaps: %v", c.Gaps)
	}
	if len(c.MissingGates) != 0 {
		t.Errorf("unexpected missing gates: %v", c.MissingGates)
	}
	if len(c.Risks) != 0 {
		t.Errorf("unexpected risks: %v", c.Risks)
	}
	if !strings.Contains(out, "No gaps found") {
		t.Errorf("expected a clean verdict:\n%s", out)
	}
}

func TestReviewVerdictAndMissingTrace(t *testing.T) {
	p, err := plan.Parse([]byte(samplePlan))
	if err != nil {
		t.Fatal(err)
	}
	snap := &store.State{
		Nodes: map[string]*store.NodeState{
			"1.1": {Status: plan.StatusDone, Attempts: 1},
			"1.2": {Status: plan.StatusDone, Attempts: 0}, // adopted
			"1.3": {Status: plan.StatusDone, Attempts: 1},
		},
	}
	r := ReviewRun(p, snap, nil)
	out := RenderReview(r)

	if r.Verdict != "done-with-gaps" {
		t.Errorf("verdict = %q, want done-with-gaps", r.Verdict)
	}
	if len(r.Hits) != 1 || r.Hits[0] != "1.2" {
		t.Errorf("hits = %v, want [1.2]", r.Hits)
	}
	if !strings.Contains(out, "MISSING") {
		t.Errorf("a run with no trace must say MISSING:\n%s", out)
	}
	for _, want := range []string{"1.1", "1.2", "1.3", "Verdict"} {
		if !strings.Contains(out, want) {
			t.Errorf("review missing %q", want)
		}
	}
}

func TestReviewReportsFailuresAndNext(t *testing.T) {
	p, _ := plan.Parse([]byte(samplePlan))
	snap := &store.State{
		Nodes: map[string]*store.NodeState{
			"1.1": {Status: plan.StatusDone, Attempts: 1},
			"1.2": {Status: plan.StatusFailed, Attempts: 2, LastError: "contract_miss: artifacts/code.go missing"},
			"1.3": {Status: plan.StatusPending},
		},
	}
	now := time.Now()
	events := []trace.Event{
		{TS: now, Node: "1.1", Event: "node.done"},
		{TS: now.Add(time.Second), Node: "1.2", Event: "node.failed"},
	}
	r := ReviewRun(p, snap, events)
	out := RenderReview(r)

	if r.Verdict != "failed" {
		t.Errorf("verdict = %q, want failed", r.Verdict)
	}
	if len(r.Failures) == 0 {
		t.Error("expected the failure to be reported")
	}
	if len(r.Next) == 0 {
		t.Error("expected a next step")
	}
	if !strings.Contains(out, "contract_miss") {
		t.Errorf("review should carry the failure reason:\n%s", out)
	}
	if r.Trace.Missing {
		t.Error("trace was provided, so it must not be reported missing")
	}
}

func TestRenderersSurviveAnEmptyPlan(t *testing.T) {
	p, err := plan.Parse([]byte("version: 1\nrun: empty\nnodes: []\n"))
	if err != nil {
		t.Fatal(err)
	}
	if out := RenderCritique(CritiquePlan(p)); out == "" {
		t.Error("critique must not be empty")
	}
	if out := RenderReview(ReviewRun(p, nil, nil)); out == "" {
		t.Error("review must not be empty")
	}
}
