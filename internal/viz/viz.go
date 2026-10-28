// Package viz projects a run (plan, state, trace) into one self-contained
// interactive HTML file.
//
// The projection is deliberately dumb: it walks the same packages the CLI uses
// (plan, store, trace) and emits a flat, serialisable document. Everything
// interactive happens in the browser, in a single canvas renderer, with no
// network access. Nothing here shells out, so `viz` works on a machine where
// the run was produced by someone else.
package viz

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/KooshaPari/dagrun/internal/plan"
	"github.com/KooshaPari/dagrun/internal/store"
	"github.com/KooshaPari/dagrun/internal/trace"
	"github.com/KooshaPari/dagrun/internal/verify"
)

// viewerHTML is the whole front end: one file, no assets, no network. It is
// embedded so `dagrun viz` produces a page that works on a machine that has
// never seen this repository.
//
//go:embed viewer.html
var viewerHTML string

// dataMarker is where the document is injected into the viewer template. It
// must appear exactly once.
const dataMarker = "__DAGRUN_DATA__"

// maxDetail bounds a verifier detail string so a pathological check output
// cannot bloat the document.
const maxDetail = 240

// Doc is the whole picture the viewer needs. It is the wire format: keep the
// JSON tags stable, they are read by viewer.html.
type Doc struct {
	Run         string     `json:"run"`
	GeneratedAt string     `json:"generated_at"`
	Intent      string     `json:"intent,omitempty"`
	Stats       Stats      `json:"stats"`
	Nodes       []Node     `json:"nodes"`
	Edges       []Edge     `json:"edges"`
	Waves       [][]string `json:"waves"`
}

// Stats is the header summary. Progress is done/(done+failed+pending+running):
// skipped nodes are structure, not work, so they never move the bar.
type Stats struct {
	Done     int     `json:"done"`
	Failed   int     `json:"failed"`
	Pending  int     `json:"pending"`
	Running  int     `json:"running"`
	Skipped  int     `json:"skipped"`
	Dynamic  int     `json:"dynamic"`
	Progress float64 `json:"progress"`
}

// Node is one rendered unit of work.
type Node struct {
	ID         string     `json:"id"`
	Kind       string     `json:"kind,omitempty"`
	Title      string     `json:"title,omitempty"`
	Status     string     `json:"status"`
	Role       string     `json:"role,omitempty"`
	Harness    string     `json:"harness,omitempty"`
	Provider   string     `json:"provider,omitempty"`
	Model      string     `json:"model,omitempty"`
	Estimate   string     `json:"estimate,omitempty"`
	Branch     string     `json:"branch,omitempty"`
	Worktree   string     `json:"worktree,omitempty"`
	LastError  string     `json:"last_error,omitempty"`
	Check      string     `json:"check,omitempty"`
	Wave       int        `json:"wave"`
	Attempts   int        `json:"attempts,omitempty"`
	DurationMS int64      `json:"duration_ms,omitempty"`
	Dynamic    bool       `json:"dynamic,omitempty"`
	Parent     string     `json:"parent,omitempty"`
	Artifacts  []Artifact `json:"artifacts,omitempty"`
}

// Artifact is one declared output and the verifier's opinion of it.
type Artifact struct {
	Path   string `json:"path"`
	OK     bool   `json:"ok"`
	Code   string `json:"code,omitempty"`
	Detail string `json:"detail,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
	Bytes  int64  `json:"bytes,omitempty"`
}

// Edge is one dependency arrow. Type is "dep" for a declared dependency and
// "fanout" for the parent -> child link of a runtime expansion.
type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Type string `json:"type"`
}

// Build reads a run directory and projects it.
func Build(dir string) (*Doc, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	planPath := filepath.Join(abs, "run.yaml")
	raw, err := os.ReadFile(planPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no plan installed at %s; run `dagrun plan FILE` first", planPath)
		}
		return nil, err
	}
	p, err := plan.Parse(raw)
	if err != nil {
		return nil, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	// Artifact paths resolve against the directory dagrun was invoked from,
	// exactly as every other command does it.
	p.Root = cwd
	st, err := store.Open(abs, p, raw, false)
	if err != nil {
		return nil, err
	}
	snap, err := st.Snapshot()
	if err != nil {
		return nil, err
	}
	events, err := trace.Merge(filepath.Join(abs, "trace"))
	if err != nil {
		return nil, err
	}
	return From(p, st.All(snap), snap, events), nil
}

// From projects an already-loaded run. Build is the filesystem front end; this
// is the pure function, which is what the tests exercise.
func From(p *plan.Plan, all []*plan.Node, snap *store.State, events []trace.Event) *Doc {
	d := &Doc{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Nodes:       make([]Node, 0, len(all)),
		Edges:       []Edge{},
		Waves:       [][]string{},
	}
	if p != nil {
		d.Run = p.Run
		d.Intent = strings.TrimSpace(p.Intent)
	}
	if snap != nil && snap.Run != "" && d.Run == "" {
		d.Run = snap.Run
	}

	known := make(map[string]bool, len(all))
	for _, n := range all {
		if n != nil && n.ID != "" {
			known[n.ID] = true
		}
	}

	wave, waves := waveIndex(all)
	tr := traceIndex(events)

	for _, n := range all {
		if n == nil || n.ID == "" {
			continue
		}
		d.Nodes = append(d.Nodes, projectNode(n, snap, tr, wave[n.ID]))
	}
	sort.SliceStable(d.Nodes, func(i, j int) bool { return plan.Less(d.Nodes[i].ID, d.Nodes[j].ID) })

	// edges: declared deps first, then fan-out links that are not already
	// represented as a dependency (a template child usually depends on its
	// parent already, and a doubled arrow would just be noise).
	seen := make(map[string]bool, len(all)*2)
	for _, n := range all {
		if n == nil || n.ID == "" {
			continue
		}
		for _, dep := range n.Deps {
			if !known[dep] {
				continue
			}
			key := dep + "\x00" + n.ID
			if seen[key] {
				continue
			}
			seen[key] = true
			d.Edges = append(d.Edges, Edge{From: dep, To: n.ID, Type: "dep"})
		}
	}
	for _, n := range all {
		if n == nil || n.ID == "" || !strings.Contains(n.ID, "/") {
			continue
		}
		parent := n.ID[:strings.LastIndex(n.ID, "/")]
		if !known[parent] {
			continue
		}
		key := parent + "\x00" + n.ID
		if seen[key] {
			continue
		}
		seen[key] = true
		d.Edges = append(d.Edges, Edge{From: parent, To: n.ID, Type: "fanout"})
	}
	sort.SliceStable(d.Edges, func(i, j int) bool {
		if d.Edges[i].From != d.Edges[j].From {
			return plan.Less(d.Edges[i].From, d.Edges[j].From)
		}
		return plan.Less(d.Edges[i].To, d.Edges[j].To)
	})

	d.Waves = waves
	d.Stats = statsOf(d.Nodes)
	return d
}

func projectNode(n *plan.Node, snap *store.State, tr *traceInfo, wave int) Node {
	out := Node{
		ID:       n.ID,
		Kind:     n.Kind,
		Title:    n.Title,
		Status:   plan.StatusPending,
		Role:     n.Role,
		Harness:  n.Harness,
		Provider: n.Provider,
		Model:    n.Model,
		Estimate: n.Estimate,
		Check:    n.Check,
		Wave:     wave,
	}
	// Fan-out children are materialised at run time and carry a hierarchical id
	// "<parent>/<slug>". The id is the only durable marker of that.
	if i := strings.LastIndex(n.ID, "/"); i > 0 {
		out.Dynamic = true
		out.Parent = n.ID[:i]
	}

	var ns *store.NodeState
	if snap != nil {
		ns = snap.Nodes[n.ID]
	}
	if ns != nil {
		if ns.Status != "" {
			out.Status = ns.Status
		}
		out.Attempts = ns.Attempts
		out.Branch = ns.Branch
		out.Worktree = ns.Worktree
		out.LastError = strings.TrimSpace(ns.LastError)
		out.DurationMS = durationMS(ns)
		if ns.Verify != nil {
			out.Artifacts = artifactsFrom(ns.Verify)
		}
	}
	if out.Attempts == 0 && tr != nil {
		out.Attempts = tr.attempts[n.ID]
	}
	if out.DurationMS == 0 && tr != nil {
		out.DurationMS = tr.duration[n.ID]
	}
	if out.Artifacts == nil {
		// Never verified: still show the declared contract so the viewer can
		// explain what this node owes, honestly marked unverified.
		out.Artifacts = declaredArtifacts(n)
	}
	return out
}

func artifactsFrom(r *verify.Report) []Artifact {
	out := make([]Artifact, 0, len(r.Artifacts))
	for _, a := range r.Artifacts {
		out = append(out, Artifact{
			Path:   a.Path,
			OK:     a.OK,
			Code:   a.Code,
			Detail: clip(a.Detail, maxDetail),
			SHA256: a.SHA256,
			Bytes:  a.Bytes,
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func declaredArtifacts(n *plan.Node) []Artifact {
	if len(n.Artifacts) == 0 {
		return nil
	}
	out := make([]Artifact, 0, len(n.Artifacts))
	for _, a := range n.Artifacts {
		out = append(out, Artifact{Path: a.Path, OK: false, Code: "unverified",
			Detail: "not verified yet"})
	}
	return out
}

func durationMS(ns *store.NodeState) int64 {
	if ns.StartedAt.IsZero() || ns.FinishedAt.IsZero() {
		return 0
	}
	d := ns.FinishedAt.Sub(ns.StartedAt)
	if d <= 0 {
		return 0
	}
	return d.Milliseconds()
}

// traceInfo is the small slice of the trace the projection actually needs:
// a fallback for attempts and duration when state.json is thin (an adopted
// node, a run whose status was written by another process).
type traceInfo struct {
	attempts map[string]int
	duration map[string]int64
}

func traceIndex(events []trace.Event) *traceInfo {
	if len(events) == 0 {
		return nil
	}
	ti := &traceInfo{attempts: map[string]int{}, duration: map[string]int64{}}
	starts := map[string]time.Time{}
	for _, e := range events {
		if e.Node == "" {
			continue
		}
		switch e.Event {
		case "node.spawn", "node.claim":
			ti.attempts[e.Node]++
			if t, ok := starts[e.Node]; !ok || e.TS.Before(t) {
				starts[e.Node] = e.TS
			}
		case "node.exit":
			if e.DurMS > 0 && e.DurMS > ti.duration[e.Node] {
				ti.duration[e.Node] = e.DurMS
			}
		case "node.done", "node.failed", "node.skipped":
			if s, ok := starts[e.Node]; ok && e.TS.After(s) {
				if ms := e.TS.Sub(s).Milliseconds(); ms > ti.duration[e.Node] {
					ti.duration[e.Node] = ms
				}
			}
		}
	}
	return ti
}

// waveIndex mirrors plan.Waves over the static *and* runtime-materialised
// nodes. plan.Waves only knows the static plan, which would stack every
// fan-out child into wave 0; a child belongs one wave after its parent.
func waveIndex(all []*plan.Node) (map[string]int, [][]string) {
	idx := make(map[string]*plan.Node, len(all))
	for _, n := range all {
		if n != nil && n.ID != "" {
			idx[n.ID] = n
		}
	}
	depth := make(map[string]int, len(idx))
	var solve func(string) int
	solve = func(id string) int {
		if d, ok := depth[id]; ok {
			return d
		}
		n := idx[id]
		if n == nil {
			return 0
		}
		depth[id] = 0 // provisional: guards a cycle that structural validation missed
		best := 0
		for _, dep := range n.Deps {
			if _, ok := idx[dep]; !ok {
				continue
			}
			if d := solve(dep) + 1; d > best {
				best = d
			}
		}
		depth[id] = best
		return best
	}
	maxD := 0
	ids := make([]string, 0, len(idx))
	for id := range idx {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return plan.Less(ids[i], ids[j]) })
	for _, id := range ids {
		if d := solve(id); d > maxD {
			maxD = d
		}
	}
	waves := make([][]string, maxD+1)
	for i := range waves {
		waves[i] = []string{}
	}
	for _, id := range ids {
		waves[depth[id]] = append(waves[depth[id]], id)
	}
	return depth, waves
}

func statsOf(nodes []Node) Stats {
	var s Stats
	var work int
	for _, n := range nodes {
		switch n.Status {
		case plan.StatusDone:
			s.Done++
			work++
		case plan.StatusFailed:
			s.Failed++
			work++
		case plan.StatusRunning:
			s.Running++
			work++
		case plan.StatusSkipped, plan.StatusCancelled:
			// Cancelled is a terminal non-failure, so it shares the skipped
			// bucket: the header has five slots and cancelling is not progress.
			s.Skipped++
		default:
			s.Pending++
			work++
		}
		if n.Dynamic {
			s.Dynamic++
		}
	}
	if work > 0 {
		s.Progress = float64(s.Done) / float64(work)
	}
	return s
}

// RenderHTML injects the document into the embedded viewer. The result is a
// single file with no external references.
func RenderHTML(doc *Doc) ([]byte, error) {
	if doc == nil {
		return nil, fmt.Errorf("viz: nil document")
	}
	if !strings.Contains(viewerHTML, dataMarker) {
		return nil, fmt.Errorf("viz: viewer template is missing the %s marker", dataMarker)
	}
	blob, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	return []byte(strings.Replace(viewerHTML, dataMarker, escapeForScript(blob), 1)), nil
}

// escapeForScript keeps arbitrary run text from terminating the JSON script
// element. Escaping every "<" (as a JSON \u003c) is the only character that
// matters: it kills "</script" and "<!--" without touching valid JSON.
func escapeForScript(b []byte) string {
	return strings.ReplaceAll(string(b), "<", `\u003c`)
}

// WriteFile renders doc and writes it to path, creating the parent directory.
func WriteFile(doc *Doc, path string) error {
	html, err := RenderHTML(doc)
	if err != nil {
		return err
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, html, 0o644)
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
