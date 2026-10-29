package verify

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/KooshaPari/dagrun/internal/plan"
)

func writeSpecPlan(t *testing.T, root, specBody, testBody string) *plan.Plan {
	t.Helper()
	yaml := `
version: 1
run: coverage
nodes:
  - id: "1.1"
    kind: SPEC
    title: the spec
    artifacts:
      - path: artifacts/SPEC.md
        min_bytes: 10
  - id: "2.1"
    kind: TEST
    title: the test
    deps: ["1.1"]
    artifacts:
      - path: artifacts/spec_test.go
        min_bytes: 10
`
	p, err := plan.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	p.Root = root
	if err := os.MkdirAll(filepath.Join(root, "artifacts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "artifacts", "SPEC.md"), []byte(specBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "artifacts", "spec_test.go"), []byte(testBody), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTESTMustCoverTheRequirementsOfItsSPEC(t *testing.T) {
	root := t.TempDir()
	spec := "Requirement: the tool must not panic\n#### Scenario: when input is junk\nthen it errors\nREQ-1 the tool rejects junk\nREQ-2 the tool logs the rejection\n"
	p := writeSpecPlan(t, root, spec, "package p\n// covers REQ-1\nfunc TestA() {}\n")

	// The test mentions only REQ-1, so the node must fail.
	n := p.ByID("2.1")
	rep := PlanNode(context.Background(), root, n, p)
	if rep.OK {
		t.Fatal("a test that misses REQ-2 must not pass")
	}
	if !contains(rep.Reason, "REQ-2") {
		t.Errorf("the reason must name the missing requirement: %q", rep.Reason)
	}

	// Cover both and it passes.
	if err := os.WriteFile(filepath.Join(root, "artifacts", "spec_test.go"),
		[]byte("package p\n// covers REQ-1 and REQ-2\nfunc TestA() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rep = PlanNode(context.Background(), root, n, p)
	if !rep.OK {
		t.Fatalf("a test covering both requirements should pass: %s", rep.Reason)
	}
}

func TestTESTCoverageIsSkippedWithoutAPlan(t *testing.T) {
	root := t.TempDir()
	spec := "Requirement: x\nREQ-1 the thing\n"
	p := writeSpecPlan(t, root, spec, "package p\nfunc TestA() {}\n")
	n := p.ByID("2.1")

	// Without the plan the coverage contract cannot run, so it is skipped
	// rather than silently passed. The node still passes its own contract.
	rep := Node(context.Background(), root, n)
	if !rep.OK {
		t.Fatalf("single-node verification must not invent a coverage failure: %s", rep.Reason)
	}
}

func TestTESTWithoutASpecAncestorHasNoCoverageRequirement(t *testing.T) {
	root := t.TempDir()
	yaml := `
version: 1
run: notest
nodes:
  - id: "1"
    kind: TEST
    artifacts:
      - path: artifacts/x_test.go
        min_bytes: 3
`
	p, err := plan.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	p.Root = root
	if err := os.MkdirAll(filepath.Join(root, "artifacts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "artifacts", "x_test.go"), []byte("package p\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rep := PlanNode(context.Background(), root, p.ByID("1"), p)
	if !rep.OK {
		t.Fatalf("a TEST with no SPEC has no coverage requirement: %s", rep.Reason)
	}
}

func contains(hay, needle string) bool {
	return len(hay) >= len(needle) && (hay == needle || indexOf(hay, needle) >= 0)
}

func indexOf(hay, needle string) int {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
