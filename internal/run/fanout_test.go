package run

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KooshaPari/dagrun/internal/plan"
	"github.com/KooshaPari/dagrun/internal/store"
	"github.com/KooshaPari/dagrun/internal/trace"
	"github.com/KooshaPari/dagrun/internal/verify"
)

// fanOutPlan is the CRun shape: a tool produces a work list, one node per unit,
// then a single reduce gate.
const fanOutPlan = `
version: 1
run: fanout
max_parallel: 4
nodes:
  - id: "4.1"
    kind: GATE
    title: discover units
    check: "mkdir -p artifacts && printf 'a\\nb\\nc\\n' > artifacts/units.txt"
    artifacts:
      - path: artifacts/units.txt
        min_bytes: 5
    discover:
      from: artifacts/units.txt
      format: lines

  - id: "4.2"
    kind: IMPL
    title: "fix {{unit}}"
    fan_out: "4.1"
    artifacts:
      - path: "artifacts/fix-{{slug}}.md"
        min_bytes: 1
    check: "test -s artifacts/fix-{{slug}}.md"

  - id: "4.3"
    kind: GATE
    title: every unit was fixed
    fan_in: "4.1"
    check: "test $(ls artifacts/fix-*.md | wc -l) -eq 3"
`

const fanOutTemplate = `sh -c "mkdir -p artifacts && printf a > artifacts/fix-a.md && printf b > artifacts/fix-b.md && printf c > artifacts/fix-c.md"`

func fanOutRig(t *testing.T, yaml string) rig {
	t.Helper()
	root := t.TempDir()
	p, err := plan.Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	p.Root = root
	if issues := p.Validate(); len(issues) > 0 {
		t.Fatalf("plan invalid: %v", issues)
	}
	dir := filepath.Join(root, ".dagrun")
	st, err := store.Open(dir, p, []byte(yaml), true)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	return rig{st: st, tr: trace.New(filepath.Join(dir, "trace"))}
}

func TestFanOutExpandsAndReduces(t *testing.T) {
	h := fanOutRig(t, fanOutPlan)
	sum := h.loop(t, fanOutTemplate, 2)

	if sum.Done != 5 || sum.Failed != 0 || sum.Pending != 0 {
		if snap, e := h.st.Snapshot(); e == nil {
			for id, ns := range snap.Nodes {
				t.Logf("node %-8s status=%-8s attempts=%d err=%s", id, ns.Status, ns.Attempts, ns.LastError)
			}
			t.Logf("expanded=%v dynamic=%d", snap.Expanded, len(snap.Dynamic))
		}
		t.Fatalf("summary = %+v, want 5 done (discover + 3 units + reduce)", sum)
	}

	snap, err := h.st.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	kids := store.Children(snap, "4.1")
	want := []string{"4.1/a", "4.1/b", "4.1/c"}
	if strings.Join(kids, ",") != strings.Join(want, ",") {
		t.Fatalf("children = %v, want %v", kids, want)
	}
	if !store.Expanded(snap, "4.1") {
		t.Error("parent not marked expanded")
	}
	for _, id := range kids {
		ns := snap.Nodes[id]
		if ns == nil || ns.Status != plan.StatusDone {
			t.Errorf("child %s status = %v", id, ns)
		}
	}
	// the reduce must depend on all three units
	fanIn := store.Resolve(snap, "4.3")
	if fanIn == nil {
		t.Fatal("reduce node was not materialised")
	}
	if len(fanIn.Deps) != 3 {
		t.Fatalf("reduce deps = %v, want the 3 children", fanIn.Deps)
	}
	// and the template itself must never have been executed
	events, err := trace.Merge(filepath.Join(h.st.Dir(), "trace"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Node == "4.2" && (e.Event == "node.claim" || e.Event == "node.spawn") {
			t.Fatalf("template node 4.2 was executed: %+v", e)
		}
	}
}

func TestFanOutIsIdempotentAcrossRuns(t *testing.T) {
	h := fanOutRig(t, fanOutPlan)
	h.loop(t, fanOutTemplate, 2)
	sum := h.loop(t, `sh -c "exit 1"`, 2)
	if sum.Done != 5 || sum.Failed != 0 {
		t.Fatalf("second run = %+v, want 5 done by adoption", sum)
	}
	snap, _ := h.st.Snapshot()
	if n := len(store.Children(snap, "4.1")); n != 3 {
		t.Fatalf("second run materialised %d children, want 3 (no duplicates)", n)
	}
	// 3 static (discover, template, reduce) + 3 materialised children
	if n := len(snap.Nodes); n != 6 {
		t.Fatalf("state has %d nodes, want 6", n)
	}
}

// TestEmptyWorkSetStillRunsTheReduce pins the semantics: zero units is a
// legitimate result, but the reduce still runs and decides. Skipping it would
// hide "the tool found nothing" from "the tool never ran".
func TestEmptyWorkSetStillRunsTheReduce(t *testing.T) {
	yaml := strings.Replace(fanOutPlan,
		`printf 'a\\nb\\nc\\n'`, `printf '\n\n\n\n\n'`, 1)
	yaml = strings.Replace(yaml,
		`check: "test $(ls artifacts/fix-*.md | wc -l) -eq 3"`,
		`check: "test $(ls artifacts/fix-*.md 2>/dev/null | wc -l) -eq 0"`, 1)
	h := fanOutRig(t, yaml)
	sum := h.loop(t, `sh -c "exit 1"`, 2)

	snap, _ := h.st.Snapshot()
	if n := len(store.Children(snap, "4.1")); n != 0 {
		t.Fatalf("empty work set materialised %d children", n)
	}
	if !store.Expanded(snap, "4.1") {
		t.Fatal("empty work set must still mark the parent expanded, else it retries forever")
	}
	if ns := snap.Nodes["4.3"]; ns == nil || ns.Status != plan.StatusDone {
		t.Fatalf("reduce status = %v, want done", ns)
	}
	if sum.Failed > 0 || sum.Pending > 0 {
		t.Fatalf("empty work set should complete cleanly: %+v", sum)
	}
}

func TestMalformedWorkSetFailsLoudly(t *testing.T) {
	yaml := strings.Replace(fanOutPlan, "format: lines", "format: json", 1)
	h := fanOutRig(t, yaml)
	_ = h.loop(t, fanOutTemplate, 2)

	snap, _ := h.st.Snapshot()
	if ns := snap.Nodes["4.1"]; ns == nil || ns.Status != plan.StatusFailed {
		t.Fatalf("discover parent = %v, want failed", ns)
	}
	if ns := snap.Nodes["4.3"]; ns == nil || ns.Status != plan.StatusFailed {
		t.Fatalf("reduce = %v, want failed (its map never ran)", ns)
	}
	if !strings.Contains(snap.Nodes["4.3"].LastError, "4.1") {
		t.Errorf("reduce error = %q, want it to name the parent", snap.Nodes["4.3"].LastError)
	}
}

func TestMaxDynamicCapsExpansion(t *testing.T) {
	yaml := strings.Replace(fanOutPlan, "max_parallel: 4", "max_parallel: 4\nmax_dynamic: 2", 1)
	h := fanOutRig(t, yaml)
	var out bytes.Buffer
	ex := New(h.st, h.tr, Options{
		Workers: 2, Owner: "test", Lease: time.Minute, ExecTemplate: fanOutTemplate, Out: &out,
	})
	_, _ = ex.Loop(context.Background())
	snap, _ := h.st.Snapshot()
	if n := len(store.Children(snap, "4.1")); n != 2 {
		t.Fatalf("materialised %d children, want 2 (max_dynamic)", n)
	}
	_ = verify.Satisfied
}

// TestMalformedWorkSetIsNeverAdoptedAsDone pins a bug that only showed up
// under load: a discover node whose work-list file already existed was
// ADOPTED as done before the expansion ran, so the malformed list was never
// parsed and the run came back green.
//
// The work set is an INPUT to the expansion, not evidence that it happened.
func TestMalformedWorkSetIsNeverAdoptedAsDone(t *testing.T) {
	yaml := strings.Replace(fanOutPlan, "format: lines", "format: json", 1)
	for i := 0; i < 5; i++ {
		h := fanOutRig(t, yaml)
		h.loop(t, fanOutTemplate, 2)
		snap, _ := h.st.Snapshot()
		ns := snap.Nodes["4.1"]
		if ns == nil || ns.Status != plan.StatusFailed {
			t.Fatalf("run %d: discover parent = %v, want failed", i, ns)
		}
	}
}

// TestDiscoverFinishingAsLoopStopsIsStillParsed pins the SECOND half of the
// same bug, which is the actual race under load.
//
// The loop reads its snapshot, expands, and only then checks whether any work
// is in flight. A worker can write "done" between those two points: the node
// ran (so adoption is no longer in play) but the snapshot the expansion saw
// still said "running", so the malformed work set was never parsed and the run
// ended green. This reproduces the stale-snapshot window deterministically:
// force the discover node done behind the executor's back, then ask the
// executor whether the run may conclude.
func TestDiscoverFinishingAsLoopStopsIsStillParsed(t *testing.T) {
	yaml := strings.Replace(fanOutPlan, "format: lines", "format: json", 1)
	h := fanOutRig(t, yaml)
	var out bytes.Buffer
	ex := New(h.st, h.tr, Options{
		Workers: 1, Owner: "test", Lease: time.Minute, ExecTemplate: fanOutTemplate, Out: &out,
	})

	// The node verified its artifact and finished, exactly as a worker would.
	if err := h.st.Force("4.1", plan.StatusDone, "", nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	changed, err := ex.expandFresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expansion reported no change; a done, unparsed work set must be a state transition")
	}
	snap, _ := h.st.Snapshot()
	if ns := snap.Nodes["4.1"]; ns == nil || ns.Status != plan.StatusFailed {
		t.Fatalf("discover parent = %v, want failed", ns)
	}
	if store.Expanded(snap, "4.1") {
		t.Error("unparseable work set must not be marked expanded")
	}
	// The transition is idempotent: a second pass has nothing left to do, so
	// the scheduler can still terminate.
	again, err := ex.expandFresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if again {
		t.Error("second expansion pass changed state; the run would never stop")
	}
}
