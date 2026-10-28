package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KooshaPari/dagrun/internal/plan"
)

// writeRun installs a minimal plan into a temporary run directory. Artifact
// paths are absolute so the test does not depend on the process working
// directory.
func writeRun(t *testing.T) (runDir, artifacts string) {
	t.Helper()
	base := t.TempDir()
	runDir = filepath.Join(base, ".dagrun")
	artifacts = filepath.Join(base, "artifacts")
	if err := os.MkdirAll(artifacts, 0o755); err != nil {
		t.Fatal(err)
	}
	planYAML := fmt.Sprintf(`version: 1
run: explain-test
nodes:
  - id: "1.1"
    kind: INTENT
    title: Freeze the scope
    artifacts:
      - path: %[1]q
        must_contain: ["func main"]
  - id: "1.2"
    kind: IMPL
    title: Write the code
    deps: ["1.1"]
    artifacts:
      - path: %[2]q
  - id: "1.3"
    kind: DOC
    title: Write the doc
    artifacts:
      - path: %[3]q
`, filepath.Join(artifacts, "code.go"), filepath.Join(artifacts, "impl.go"), filepath.Join(artifacts, "doc.md"))
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "run.yaml"), []byte(planYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	return runDir, artifacts
}

func mustExplain(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var buf bytes.Buffer
	code, err := explain(args, &buf)
	if err != nil {
		t.Fatalf("explain(%v) returned error: %v", args, err)
	}
	return code, buf.String()
}

func TestExplainFailingNodeNamesPredicateAndFix(t *testing.T) {
	runDir, artifacts := writeRun(t)

	code, out := mustExplain(t, "1.1", "--json", "--dir", runDir)
	if code != 1 {
		t.Fatalf("exit = %d, want 1 for a node that is not done", code)
	}
	var got explainNode
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if got.Node != "1.1" || got.Status != plan.StatusPending || got.OK {
		t.Fatalf("unexpected identity/status: %+v", got)
	}
	if len(got.Findings) != 1 {
		t.Fatalf("findings = %d, want 1", len(got.Findings))
	}
	f := got.Findings[0]
	if f.Path != filepath.Join(artifacts, "code.go") || f.OK || f.Code != "missing" {
		t.Fatalf("unexpected finding: %+v", f)
	}
	if !strings.Contains(got.Verdict, "1.1 PENDING") || !strings.Contains(got.Verdict, "missing") {
		t.Fatalf("verdict = %q, want the node id, status and failing code", got.Verdict)
	}
	if !strings.Contains(got.Hint, "create") || !strings.Contains(got.Hint, "dagrun done 1.1") {
		t.Fatalf("hint = %q, want a create instruction", got.Hint)
	}
	if got.BlockedBy == nil {
		t.Fatalf("blocked_by must be an empty array, not null")
	}
}

func TestExplainTextViewHasVerdictAndFix(t *testing.T) {
	runDir, _ := writeRun(t)

	code, out := mustExplain(t, "1.1", "--dir", runDir)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	for _, want := range []string{"node 1.1", "status pending", "attempts 0/2", "artifacts (1)", "fix:"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n%s", want, out)
		}
	}
}

func TestExplainPendingNodeShowsBlockingChain(t *testing.T) {
	runDir, _ := writeRun(t)

	code, out := mustExplain(t, "1.2", "--json", "--dir", runDir)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	var got explainNode
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("bad JSON: %v\n%s", err, out)
	}
	if len(got.BlockedBy) != 1 || got.BlockedBy[0].ID != "1.1" || got.BlockedBy[0].Status != plan.StatusPending {
		t.Fatalf("blocked_by = %+v, want 1.1 pending", got.BlockedBy)
	}
	if !strings.Contains(got.Hint, "finish 1.1 first") {
		t.Fatalf("hint = %q, want the blocking dep as the next action", got.Hint)
	}
}

func TestExplainSatisfiedButUnattested(t *testing.T) {
	runDir, artifacts := writeRun(t)
	if err := os.WriteFile(filepath.Join(artifacts, "doc.md"), []byte("# doc\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	code, out := mustExplain(t, "1.3", "--json", "--dir", runDir)
	if code != 1 {
		t.Fatalf("exit = %d, want 1 while the node is not attested", code)
	}
	var got explainNode
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("bad JSON: %v\n%s", err, out)
	}
	if !got.Findings[0].OK || got.Findings[0].SHA256 == "" {
		t.Fatalf("finding = %+v, want ok with a hash", got.Findings[0])
	}
	if !strings.Contains(got.Hint, "dagrun done 1.3") {
		t.Fatalf("hint = %q, want the attest action", got.Hint)
	}
}

func TestExplainDoneNodeExitsZero(t *testing.T) {
	runDir, artifacts := writeRun(t)
	doc := filepath.Join(artifacts, "doc.md")
	if err := os.WriteFile(doc, []byte("# doc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, st, _, err := openRun(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Force("1.3", plan.StatusDone, "", nil, time.Now()); err != nil {
		t.Fatal(err)
	}

	code, out := mustExplain(t, "1.3", "--json", "--dir", runDir)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 for a done node", code)
	}
	var got explainNode
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("bad JSON: %v\n%s", err, out)
	}
	if !got.OK || got.Status != plan.StatusDone {
		t.Fatalf("unexpected view: %+v", got)
	}
	if !strings.Contains(got.Verdict, "DONE") {
		t.Fatalf("verdict = %q, want DONE", got.Verdict)
	}
	if got.Hint != "" {
		t.Fatalf("hint = %q, want empty for a done node", got.Hint)
	}

	// The same node in text mode prints the hash, not a fix line.
	_, text := mustExplain(t, "1.3", "--dir", runDir)
	if !strings.Contains(text, "sha256:") {
		t.Fatalf("text output missing artifact hash:\n%s", text)
	}
}

func TestExplainUnknownNodeSuggestsClosest(t *testing.T) {
	runDir, _ := writeRun(t)

	var buf bytes.Buffer
	code, err := explain([]string{"1.9", "--dir", runDir}, &buf)
	if err == nil {
		t.Fatalf("explain(unknown) returned no error; code=%d out=%s", code, buf.String())
	}
	if !strings.Contains(err.Error(), "unknown node") || !strings.Contains(err.Error(), "closest ids") {
		t.Fatalf("error = %v, want unknown-node with suggestions", err)
	}
	if !strings.Contains(err.Error(), "1.1") {
		t.Fatalf("error = %v, want a suggested id", err)
	}
}

func TestExplainRequiresNode(t *testing.T) {
	runDir, _ := writeRun(t)
	var buf bytes.Buffer
	if _, err := explain([]string{"--dir", runDir}, &buf); err == nil {
		t.Fatal("explain with no node returned no error")
	}
}
