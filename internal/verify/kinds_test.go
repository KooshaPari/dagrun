package verify

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/KooshaPari/dagrun/internal/plan"
)

func writeKind(t *testing.T, root, rel, body string) {
	t.Helper()
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func kindNode(kind, rel string) *plan.Node {
	return &plan.Node{ID: "x", Kind: kind, Artifacts: []plan.Artifact{{Path: rel}}}
}

func TestKindCheckSPEC(t *testing.T) {
	root := t.TempDir()
	writeKind(t, root, "s.md", "Requirement: the tool must not panic\n#### Scenario: when input is junk\nthen it returns an error\n")
	if r := KindCheck(kindNode("SPEC", "s.md"), root); r != nil && !r.OK {
		t.Fatalf("SPEC should pass: %+v", r)
	}
	writeKind(t, root, "bad.md", "just some prose with no requirement\n")
	r := KindCheck(kindNode("SPEC", "bad.md"), root)
	if r == nil || r.OK {
		t.Fatal("SPEC without a requirement must fail")
	}
	if r.Code != "spec_no_requirement" {
		t.Errorf("code = %q", r.Code)
	}
}

func TestKindCheckRESEARCH(t *testing.T) {
	root := t.TempDir()
	writeKind(t, root, "r.md", "https://a.invalid/x\nhttps://b.invalid/y\nDecision: use A\nAlternatives: B, C\n")
	if r := KindCheck(kindNode("RESEARCH", "r.md"), root); r != nil && !r.OK {
		t.Fatalf("RESEARCH should pass: %+v", r)
	}
	writeKind(t, root, "thin.md", "https://a.invalid/x\n")
	r := KindCheck(kindNode("RESEARCH", "thin.md"), root)
	if r == nil || r.OK {
		t.Fatal("RESEARCH with one URL must fail")
	}
	writeKind(t, root, "nodec.md", "https://a.invalid/x\nhttps://b.invalid/y\n")
	r = KindCheck(kindNode("RESEARCH", "nodec.md"), root)
	if r == nil || r.OK || r.Code != "research_no_decision" {
		t.Fatalf("expected research_no_decision, got %+v", r)
	}
}

func TestKindCheckINTENT(t *testing.T) {
	root := t.TempDir()
	writeKind(t, root, "i.md", "Acceptance Criteria\n- one\n- two\n- three\n")
	if r := KindCheck(kindNode("INTENT", "i.md"), root); r != nil && !r.OK {
		t.Fatalf("INTENT should pass: %+v", r)
	}
	writeKind(t, root, "thin.md", "Acceptance Criteria\n- one\n")
	r := KindCheck(kindNode("INTENT", "thin.md"), root)
	if r == nil || r.OK {
		t.Fatal("INTENT with one bullet must fail")
	}
}

func TestKindCheckSOTA(t *testing.T) {
	root := t.TempDir()
	writeKind(t, root, "s.md", "## Candidates\n- **Option A** — simple\n- **Option B** — complete\n\n## Axes\n- axis: cost\n- axis: speed\n- axis: size\n\nVerdict: adopt B\n")
	if r := KindCheck(kindNode("SOTA", "s.md"), root); r != nil && !r.OK {
		t.Fatalf("SOTA should pass: %+v", r)
	}
	writeKind(t, root, "one.md", "## Candidates\n- **Option A**\n\nVerdict: adopt A\n")
	r := KindCheck(kindNode("SOTA", "one.md"), root)
	if r == nil || r.OK {
		t.Fatal("SOTA with one candidate must fail")
	}
}

func TestKindCheckUIT(t *testing.T) {
	root := t.TempDir()
	writeKind(t, root, "u.md", "States: empty, loading, error.\nAccessibility: aria labels.\n")
	if r := KindCheck(kindNode("UI", "u.md"), root); r != nil && !r.OK {
		t.Fatalf("UI should pass: %+v", r)
	}
	writeKind(t, root, "bad.md", "A screen.\n")
	r := KindCheck(kindNode("UI", "bad.md"), root)
	if r == nil || r.OK {
		t.Fatal("UI without states must fail")
	}
}

func TestKindCheckDOCBrokenLink(t *testing.T) {
	root := t.TempDir()
	writeKind(t, root, "d.md", "see [other](missing.md)\n")
	r := KindCheck(kindNode("DOC", "d.md"), root)
	if r == nil || r.OK || r.Code != "doc_broken_link" {
		t.Fatalf("expected doc_broken_link, got %+v", r)
	}
	writeKind(t, root, "ok.md", "see [other](present.md) and [web](https://x.invalid)\n")
	writeKind(t, root, "present.md", "here\n")
	if r := KindCheck(kindNode("DOC", "ok.md"), root); r != nil && !r.OK {
		t.Fatalf("DOC with a resolvable link should pass: %+v", r)
	}
}

func TestKindCheckNoContractForOtherKinds(t *testing.T) {
	for _, kind := range []string{"PLAN", "IMPL", "CODE", "GATE", "OTHER"} {
		if r := KindCheck(kindNode(kind, "x.md"), t.TempDir()); r != nil {
			t.Errorf("%s should have no extra contract, got %+v", kind, r)
		}
	}
}

func TestKindCheckFailureIsFoldedIntoTheReport(t *testing.T) {
	root := t.TempDir()
	writeKind(t, root, "i.md", "Acceptance Criteria\n- one\n")
	n := kindNode("INTENT", "i.md")
	rep := Node(context.Background(), root, n)
	if rep.OK {
		t.Fatal("a thin INTENT must fail the node")
	}
	if rep.Reason == "" {
		t.Error("the reason must name the failing contract")
	}
}
