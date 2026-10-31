package run

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KooshaPari/dagrun/internal/plan"
	"github.com/KooshaPari/dagrun/internal/store"
	"github.com/KooshaPari/dagrun/internal/trace"
)

// A dependency chain must carry the parent's ACTUAL BYTES to the child.
//
// Nothing in the suite proved this. The prompt builder emits an "Inputs
// already produced" section naming the dependency's artifacts, but "the prompt
// mentions the path" is not "the child can act on the content". A regression
// there would silently break every multi-step plan: the child would invent
// content, the parent's contract would still verify, and the run would look
// green while carrying nothing across the edge.
//
// The exec template splits on whitespace and strips double quotes, so the
// template is a plain shell script written to a file, and the prompt arrives
// on STDIN. The script branches on the prompt because the prompt names the
// node's artifact path.
const chainPlan = `
version: 1
run: chain
max_parallel: 1
nodes:
  - id: "first"
    kind: DOC
    title: produce the parent artifact
    artifacts: [{path: artifacts/notes.md, min_bytes: 10}]
  - id: "second"
    kind: DOC
    title: consume the parent artifact
    deps: ["first"]
    artifacts: [{path: artifacts/summary.md, min_bytes: 10, must_contain: ["CHAIN-OK"]}]
`

// The child's prompt ALSO names the parent's path, in its "Inputs already
// produced" section, so the branch order matters: the child's own path must
// be tested FIRST. Matching the parent's path first runs the parent's branch
// for the child, and the child then never writes its own artifact.
//
// That is not a hypothetical: the first version of this test had the order
// backwards and the child failed with "artifacts/summary.md: missing file".
const chainScript = `#!/bin/sh
mkdir -p artifacts
prompt=$(cat)
case "$prompt" in
  *summary.md*) cat artifacts/notes.md > artifacts/summary.md; printf '\nCHAIN-OK' >> artifacts/summary.md ;;
  *notes.md*)  printf 'PARENT-BODY-MARKER-12345' > artifacts/notes.md ;;
esac
exit 0
`

func chainRig(t *testing.T) (*store.Store, *trace.Writer, string) {
	t.Helper()
	root := t.TempDir()
	p, err := plan.Parse([]byte(chainPlan))
	if err != nil {
		t.Fatal(err)
	}
	p.Root = root
	dir := filepath.Join(root, ".dagrun")
	st, err := store.Open(dir, p, []byte(chainPlan), true)
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "chain.sh")
	if err := os.WriteFile(script, []byte(chainScript), 0o755); err != nil {
		t.Fatal(err)
	}
	return st, trace.New(filepath.Join(dir, "trace")), root
}

func TestChainChildReadsParentArtifact(t *testing.T) {
	st, tr, root := chainRig(t)

	var out bytes.Buffer
	ex := New(st, tr, Options{
		Workers:      1,
		Owner:        "test",
		Lease:        time.Minute,
		ExecTemplate: "sh -c " + filepath.Join(root, "chain.sh") + " {stdin}",
		Out:          &out,
	})
	sum, err := ex.Loop(context.Background())
	if err != nil {
		t.Fatalf("loop: %v (out: %s)", err, out.String())
	}
	if sum.Done != 2 {
		t.Fatalf("summary = %+v, want 2 done (out: %s)", sum, out.String())
	}

	summary, err := os.ReadFile(filepath.Join(root, "artifacts", "summary.md"))
	if err != nil {
		t.Fatalf("summary.md: %v", err)
	}
	text := string(summary)

	// The child's own contract.
	if !strings.Contains(text, "CHAIN-OK") {
		t.Fatalf("summary.md lacks the required marker: %q", text)
	}
	// THE POINT: the parent's actual bytes crossed the dependency edge.
	if !strings.Contains(text, "PARENT-BODY-MARKER-12345") {
		t.Fatalf("summary.md does not contain the parent's bytes: %q\n"+
			"the child did not read the parent's artifact", text)
	}

	// The order: the parent finished before the child started.
	snap, _ := st.Snapshot()
	first := snap.Nodes["first"]
	second := snap.Nodes["second"]
	if first.FinishedAt.IsZero() || second.StartedAt.IsZero() {
		t.Fatalf("missing timestamps: first.finished=%v second.started=%v", first.FinishedAt, second.StartedAt)
	}
	if second.StartedAt.Before(first.FinishedAt) {
		t.Fatalf("the child started at %v, before the parent finished at %v",
			second.StartedAt, first.FinishedAt)
	}
}

// The parent's artifact is what the child sees, so POISONING the parent's file
// must change what the child can produce.
//
// The parent's own predicate only checked the file existed and was long
// enough. Truncating it afterwards leaves the parent's contract satisfied
// while destroying its content. If the child were reading a description of
// the parent rather than its bytes, the child's output would still contain
// the parent's marker — and this test would catch exactly that.
func TestChainChildSeesAPoisonedParentArtifact(t *testing.T) {
	st, tr, root := chainRig(t)

	// A template whose child branch clobbers the parent's file and THEN reads
	// it. That ordering is what makes the test deterministic: the parent
	// finishes cleanly, and the child demonstrably copies whatever is on disk
	// at the moment it reads, not a description of what the parent wrote.
	//
	// The first version poisoned the file from the PARENT's branch, after a
	// sleep. That was wrong twice: the parent then failed its own min_bytes
	// contract (the poisoned file was too short), so the child never ran and
	// the test asserted against a file that did not exist.
	script := filepath.Join(root, "poison.sh")
	body := `#!/bin/sh
mkdir -p artifacts
prompt=$(cat)
case "$prompt" in
  *summary.md*)
      printf 'POISONED' > artifacts/notes.md
      cat artifacts/notes.md > artifacts/summary.md
      printf '\nCHAIN-OK' >> artifacts/summary.md
      ;;
  *notes.md*)
      printf 'PARENT-BODY-MARKER-12345' > artifacts/notes.md
      ;;
esac
exit 0
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	ex := New(st, tr, Options{
		Workers:      1,
		Owner:        "test",
		Lease:        time.Minute,
		ExecTemplate: "sh -c " + script + " {stdin}",
		Out:          &out,
	})
	if _, err := ex.Loop(context.Background()); err != nil {
		t.Fatalf("loop: %v (out: %s)", err, out.String())
	}

	summary, err := os.ReadFile(filepath.Join(root, "artifacts", "summary.md"))
	if err != nil {
		t.Fatalf("summary.md: %v", err)
	}
	text := string(summary)

	// The child's OWN contract still holds: it wrote what it was told to.
	if !strings.Contains(text, "CHAIN-OK") {
		t.Fatalf("summary.md lacks the required marker: %q", text)
	}
	// And the content it copied is what was ACTUALLY on disk when it ran,
	// not what the parent originally wrote.
	if !strings.Contains(text, "POISONED") {
		t.Fatalf("summary.md = %q, want the poisoned content: the child read a "+
			"description of the parent rather than its bytes", text)
	}
	if strings.Contains(text, "PARENT-BODY-MARKER-12345") {
		t.Fatalf("summary.md = %q, still holds the parent's ORIGINAL bytes "+
			"after the file was clobbered", text)
	}
}
