package viz

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/KooshaPari/dagrun/internal/plan"
	"github.com/KooshaPari/dagrun/internal/store"
	"github.com/KooshaPari/dagrun/internal/trace"
	"github.com/KooshaPari/dagrun/internal/verify"
)

// extractData pulls the embedded document out of a rendered page. It fails the
// test rather than returning an empty slice: a page with no data block is broken
// even if the HTML around it looks fine.
func extractData(t *testing.T, html []byte) []byte {
	t.Helper()
	const open = `id="dagrun-data">`
	s := string(html)
	i := strings.Index(s, open)
	if i < 0 {
		t.Fatal("rendered HTML has no dagrun-data script block")
	}
	rest := s[i+len(open):]
	j := strings.Index(rest, "</script>")
	if j < 0 {
		t.Fatal("dagrun-data script block is not closed")
	}
	return []byte(rest[:j])
}

// tinyRun builds a plan, a state and a trace with one of everything the viewer
// reads: a done node with a verify report, a failed node with an error, a
// pending node, and a fan-out child.
func tinyRun(t *testing.T) (*plan.Plan, []*plan.Node, *store.State, []trace.Event) {
	t.Helper()
	raw := `version: 1
run: viz-fixture
intent: prove the viewer renders a run
defaults:
  estimate: 5m
nodes:
  - id: "1.1"
    kind: INTENT
    title: Freeze scope
    artifacts:
      - path: artifacts/INTENT.md
  - id: "1.2"
    kind: SPEC
    title: Write the contract
    deps: ["1.1"]
    artifacts:
      - path: artifacts/SPEC.md
  - id: "1.2/handle-a"
    kind: CODE
    title: handle a
    deps: ["1.2"]
  - id: "1.3"
    kind: GATE
    title: Build gate
    deps: ["1.2"]
    check: "go build ./..."
  - id: "1.4"
    kind: DOC
    title: Write it down
    deps: ["1.3"]
`
	p, err := plan.Parse([]byte(raw))
	if err != nil {
		t.Fatalf("parse fixture plan: %v", err)
	}
	p.Root = t.TempDir()

	start := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	snap := &store.State{
		Version: 1,
		Run:     p.Run,
		Nodes: map[string]*store.NodeState{
			"1.1": {
				Status:     plan.StatusDone,
				Attempts:   1,
				StartedAt:  start,
				FinishedAt: start.Add(90 * time.Second),
				Verify: &verify.Report{
					NodeID: "1.1",
					OK:     true,
					Artifacts: []verify.ArtifactResult{{
						Path: "artifacts/INTENT.md", OK: true, Bytes: 812,
						SHA256: "0123456789abcdef0123456789abcdef",
					}},
				},
			},
			"1.2": {
				Status:    plan.StatusFailed,
				Attempts:  2,
				LastError: "artifact artifacts/SPEC.md: min_bytes 400, got 12",
				Branch:    "dagrun/1.2",
				Worktree:  "/tmp/wt-1.2",
			},
			"1.2/handle-a": {Status: plan.StatusDone, Attempts: 1},
			"1.3":          {Status: plan.StatusRunning, Attempts: 1, StartedAt: start},
		},
	}
	events := []trace.Event{
		{TS: start, Node: "1.1", Event: "node.spawn", Attempt: 1},
		{TS: start.Add(90 * time.Second), Node: "1.1", Event: "node.done"},
		{TS: start, Node: "1.2", Event: "node.spawn", Attempt: 1},
		{TS: start.Add(60 * time.Second), Node: "1.2", Event: "node.exit", DurMS: 60000},
		{TS: start, Node: "1.2", Event: "node.spawn", Attempt: 2},
	}
	return p, p.Nodes, snap, events
}

func TestRenderContainsRunAndEveryNode(t *testing.T) {
	p, nodes, snap, events := tinyRun(t)
	doc := From(p, nodes, snap, events)

	html, err := RenderHTML(doc)
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	s := string(html)

	if !strings.Contains(s, `viz-fixture`) {
		t.Error("rendered HTML does not contain the run name")
	}
	for _, n := range doc.Nodes {
		if !strings.Contains(s, n.ID) {
			t.Errorf("rendered HTML does not contain node id %q", n.ID)
		}
	}

	// one self-contained file: no external references at all
	for _, bad := range []string{"http://", "https://", "<link ", "<img "} {
		if strings.Contains(s, bad) {
			t.Errorf("rendered HTML contains an external reference (%q); it must work offline", bad)
		}
	}
	if !strings.Contains(s, "<canvas") {
		t.Error("rendered HTML has no canvas renderer")
	}
	if !strings.Contains(s, "requestAnimationFrame") {
		t.Error("rendered HTML has no animation loop")
	}

	// the required controls have to survive any template edit
	for _, want := range []string{
		`id="search"`,            // search box
		`id="chips"`,             // status filter chips
		`id="fit"`,               // fit button
		`id="panel"`,             // side panel
		`id="crit"`,              // critical path slot
		"prefers-reduced-motion", // motion preference
		"aria-label",             // screen-reader labels
		"wheel",                  // wheel zoom
		"dblclick",               // double-click zoom
		"pointermove",            // drag + hover
	} {
		if !strings.Contains(s, want) {
			t.Errorf("viewer is missing %q", want)
		}
	}
	if n := strings.Count(string(viewerHTML), dataMarker); n != 1 {
		t.Errorf("viewer template has %d data markers, want exactly 1", n)
	}
}

func TestEmbeddedJSONRoundTrips(t *testing.T) {
	p, nodes, snap, events := tinyRun(t)
	doc := From(p, nodes, snap, events)

	html, err := RenderHTML(doc)
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	blob := extractData(t, html)

	var got Doc
	if err := json.Unmarshal(blob, &got); err != nil {
		t.Fatalf("embedded JSON does not parse: %v\nfirst 200 bytes: %.200s", err, blob)
	}
	if !reflect.DeepEqual(doc, &got) {
		jb, _ := json.MarshalIndent(doc, "", "  ")
		gb, _ := json.MarshalIndent(&got, "", "  ")
		t.Fatalf("round-trip changed the document\nwant:\n%s\n\ngot:\n%s", jb, gb)
	}

	// the projection itself has to be sane, not just serialisable
	if got.Stats.Done != 2 || got.Stats.Failed != 1 || got.Stats.Running != 1 || got.Stats.Pending != 1 {
		t.Fatalf("unexpected stats: %+v", got.Stats)
	}
	if got.Stats.Dynamic != 1 {
		t.Fatalf("expected 1 dynamic node, got %d", got.Stats.Dynamic)
	}
	if len(got.Edges) == 0 {
		t.Fatal("expected dependency edges")
	}
	child := findNode(got.Nodes, "1.2/handle-a")
	if child == nil {
		t.Fatal("fan-out child missing from the document")
	}
	if child.Parent != "1.2" || !child.Dynamic {
		t.Fatalf("fan-out child not marked: %+v", child)
	}
	if w := waveOf(got.Waves, "1.2/handle-a"); w != waveOf(got.Waves, "1.2")+1 {
		t.Fatalf("fan-out child wave = %d, want parent wave + 1", w)
	}
	if n := findNode(got.Nodes, "1.1"); n == nil || n.DurationMS != 90000 {
		t.Fatalf("expected 90s duration from state, got %+v", n)
	}
	if n := findNode(got.Nodes, "1.2"); n == nil || n.Attempts != 2 || n.LastError == "" {
		t.Fatalf("failed node lost its attempts or error: %+v", n)
	}
}

func TestThreeHundredNodesStaySmall(t *testing.T) {
	n := 300
	doc := &Doc{
		Run:         "big-run",
		GeneratedAt: "2026-10-28T09:00:00Z",
		Intent:      "a wide fan-out run",
		Nodes:       make([]Node, 0, n),
		Edges:       make([]Edge, 0, n),
	}
	waves := [][]string{}
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("2.%d", i)
		w := i / 25
		for len(waves) <= w {
			waves = append(waves, []string{})
		}
		waves[w] = append(waves[w], id)
		doc.Nodes = append(doc.Nodes, Node{
			ID:       id,
			Kind:     "IMPL",
			Title:    "handle unit " + fmt.Sprint(i),
			Status:   []string{"done", "pending", "running", "failed", "skipped"}[i%5],
			Estimate: "10m",
			Wave:     w,
			Artifacts: []Artifact{{
				Path: fmt.Sprintf("artifacts/2.%d.md", i), OK: i%5 == 0, Bytes: 4096,
			}},
		})
		if i > 0 {
			doc.Edges = append(doc.Edges, Edge{From: fmt.Sprintf("2.%d", i-1), To: id, Type: "dep"})
		}
	}
	doc.Waves = waves
	doc.Stats = statsOf(doc.Nodes)

	html, err := RenderHTML(doc)
	if err != nil {
		t.Fatalf("RenderHTML(300 nodes): %v", err)
	}
	const limit = 2 * 1024 * 1024
	if len(html) >= limit {
		t.Fatalf("300-node page is %d bytes, want under %d", len(html), limit)
	}
	if !strings.Contains(string(html), `"id":"2.299"`) {
		t.Fatal("last node missing from the 300-node page")
	}
	blob := extractData(t, html)
	var back Doc
	if err := json.Unmarshal(blob, &back); err != nil {
		t.Fatalf("300-node JSON does not parse: %v", err)
	}
	if len(back.Nodes) != n {
		t.Fatalf("round-trip lost nodes: %d != %d", len(back.Nodes), n)
	}
}

func TestEscapeProtectsScriptBlock(t *testing.T) {
	doc := &Doc{
		Run:   `</script><script>alert(1)</script>`,
		Nodes: []Node{{ID: "1.1", Status: "pending", Title: "<!-- comment -->"}},
		Edges: []Edge{},
		Waves: [][]string{{"1.1"}},
	}
	html, err := RenderHTML(doc)
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	blob := string(extractData(t, html))
	if strings.Contains(blob, "</script") {
		t.Fatal("a node value escaped the JSON script block")
	}
	var back Doc
	if err := json.Unmarshal([]byte(blob), &back); err != nil {
		t.Fatalf("escaped JSON does not parse: %v", err)
	}
	if back.Run != doc.Run {
		t.Fatalf("escaped run name came back as %q", back.Run)
	}
}

func TestStatsProgressIgnoresSkipped(t *testing.T) {
	s := statsOf([]Node{
		{Status: plan.StatusDone},
		{Status: plan.StatusDone},
		{Status: plan.StatusFailed},
		{Status: plan.StatusPending},
		{Status: plan.StatusSkipped},
		{Status: plan.StatusCancelled},
	})
	if s.Progress != 0.5 {
		t.Fatalf("progress = %v, want 0.5 (2 of 4 units of work)", s.Progress)
	}
	if s.Skipped != 2 {
		t.Fatalf("skipped = %d, want 2 (cancelled folds in)", s.Skipped)
	}
}

func findNode(nodes []Node, id string) *Node {
	for i := range nodes {
		if nodes[i].ID == id {
			return &nodes[i]
		}
	}
	return nil
}

func waveOf(waves [][]string, id string) int {
	for w, list := range waves {
		for _, x := range list {
			if x == id {
				return w
			}
		}
	}
	return -1
}
