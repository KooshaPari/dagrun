package tracera

import (
	"encoding/json"
	"testing"

	"github.com/KooshaPari/dagrun/internal/plan"
	"github.com/KooshaPari/dagrun/internal/store"
	"github.com/KooshaPari/dagrun/internal/verify"
)

func makePlanForImport(t *testing.T) *plan.Plan {
	t.Helper()
	p := &plan.Plan{
		Version: 1,
		Run:     "chain-run",
		Nodes: []*plan.Node{
			{ID: "A", Kind: "IMPL", Title: "A", Artifacts: []plan.Artifact{{Path: "a.txt"}}},
			{ID: "B", Kind: "IMPL", Title: "B", Deps: []string{"A"}, Artifacts: []plan.Artifact{{Path: "b.txt"}}},
			{ID: "C", Kind: "IMPL", Title: "C", Deps: []string{"B"}, Artifacts: []plan.Artifact{{Path: "c.txt"}}},
		},
	}
	return p
}



func buildStateForImport(p *plan.Plan) *store.State {
	st := &store.State{Run: p.Run, Nodes: map[string]*store.NodeState{}}
	for _, n := range p.Nodes {
		arts := []verify.ArtifactResult{}
		for _, a := range n.Artifacts {
			arts = append(arts, verify.ArtifactResult{Path: a.Path, OK: true, Bytes: 10, SHA256: "abc"})
		}
		st.Nodes[n.ID] = &store.NodeState{Status: plan.StatusDone, Verify: &verify.Report{NodeID: n.ID, OK: true, Artifacts: arts}}
	}
	return st
}

func TestImportRoundTrip(t *testing.T) {
	p := makePlanForImport(t)
	st := buildStateForImport(p)
	payload, err := BuildPayload(p, st, "tracera")
	if err != nil {
		t.Fatal(err)
	}
	res, err := Import(payload)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.Plan == nil {
		t.Fatal("nil plan")
	}
	if len(res.Plan.Nodes) != 3 {
		t.Fatalf("nodes = %d", len(res.Plan.Nodes))
	}
	ids := map[string]bool{}
	for _, n := range res.Plan.Nodes {
		ids[n.ID] = true
	}
	for _, id := range []string{"A", "B", "C"} {
		if !ids[id] {
			t.Fatalf("missing node %s", id)
		}
	}
	// dep direction: A->B->C means B depends on A, C depends on B
	byID := map[string]*plan.Node{}
	for _, n := range res.Plan.Nodes {
		byID[n.ID] = n
	}
	if !contains(byID["B"].Deps, "A") {
		t.Fatalf("B deps = %v, want A", byID["B"].Deps)
	}
	if !contains(byID["C"].Deps, "B") {
		t.Fatalf("C deps = %v, want B", byID["C"].Deps)
	}
	if contains(byID["A"].Deps, "B") || contains(byID["B"].Deps, "C") {
		t.Fatal("dep direction reversed")
	}
	// artifacts
	if len(byID["A"].Artifacts) == 0 || byID["A"].Artifacts[0].Path != "a.txt" {
		t.Fatalf("A artifacts = %+v", byID["A"].Artifacts)
	}
	if len(byID["B"].Artifacts) == 0 || byID["B"].Artifacts[0].Path != "b.txt" {
		t.Fatalf("B artifacts = %+v", byID["B"].Artifacts)
	}
	if len(byID["C"].Artifacts) == 0 || byID["C"].Artifacts[0].Path != "c.txt" {
		t.Fatalf("C artifacts = %+v", byID["C"].Artifacts)
	}
}

func TestImportBogusKindFallback(t *testing.T) {
	payload := Payload{Items: []Item{
		{ID: "node:X", Kind: "node", Type: "BOGUS", Title: "X"},
	}}
	// make verifiable
	payload.Items[0].Title = "X"
	// add artifact via item? artifact is separate; mark optional by providing check? no. Use artifact item.
	payload.Items = append(payload.Items, Item{ID: "artifact:X/p.txt", Kind: "artifact", From: "X", Path: "p.txt"})
	res, err := Import(payload)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	n := res.Plan.Nodes[0]
	if n.Kind != "OTHER" {
		t.Fatalf("kind = %s", n.Kind)
	}
	found := false
	for _, d := range res.Dropped {
		if containsStr(d, "BOGUS") {
			found = true
		}
	}
	if !found {
		t.Fatalf("BOGUS not in Dropped: %v", res.Dropped)
	}
}

func TestImportOrphanEdgeDropped(t *testing.T) {
	payload := Payload{Items: []Item{
		{ID: "node:A", Kind: "node", Type: "IMPL", Title: "A"},
		{ID: "node:B", Kind: "node", Type: "IMPL", Title: "B"},
		{ID: "edge:A->ghost", Kind: "edge", From: "A", To: "ghost"},
		{ID: "artifact:A/a.txt", Kind: "artifact", From: "A", Path: "a.txt"},
		{ID: "artifact:B/b.txt", Kind: "artifact", From: "B", Path: "b.txt"},
	}}
	res, err := Import(payload)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	byID := map[string]*plan.Node{}
	for _, n := range res.Plan.Nodes {
		byID[n.ID] = n
	}
	for _, n := range res.Plan.Nodes {
		if contains(n.Deps, "ghost") {
			t.Fatal("orphan dep present")
		}
	}
	found := false
	for _, d := range res.Dropped {
		if containsStr(d, "ghost") {
			found = true
		}
	}
	if !found {
		t.Fatalf("orphan edge not dropped: %v", res.Dropped)
	}
	_ = byID
}

func TestImportOrphanArtifactDropped(t *testing.T) {
	payload := Payload{Items: []Item{
		{ID: "node:A", Kind: "node", Type: "IMPL", Title: "A"},
		{ID: "artifact:ghost/p.txt", Kind: "artifact", From: "ghost", Path: "p.txt"},
	}}
	// make A verifiable
	payload.Items = append(payload.Items, Item{ID: "artifact:A/a.txt", Kind: "artifact", From: "A", Path: "a.txt"})
	res, err := Import(payload)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	found := false
	for _, d := range res.Dropped {
		if containsStr(d, "ghost") {
			found = true
		}
	}
	if !found {
		t.Fatalf("orphan artifact not dropped: %v", res.Dropped)
	}
}

func TestImportValidates(t *testing.T) {
	p := makePlanForImport(t)
	st := buildStateForImport(p)
	payload, _ := BuildPayload(p, st, "tracera")
	res, err := Import(payload)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if issues := res.Plan.Validate(); len(issues) != 0 {
		t.Fatalf("issues: %v", issues)
	}
}

func TestImportJSONBothForms(t *testing.T) {
	p := makePlanForImport(t)
	st := buildStateForImport(p)
	payload, _ := BuildPayload(p, st, "tracera")

	// object form
	b, _ := json.Marshal(payload)
	res, err := ImportJSON(b)
	if err != nil {
		t.Fatalf("object form: %v", err)
	}
	if len(res.Plan.Nodes) != 3 {
		t.Fatalf("object form nodes = %d", len(res.Plan.Nodes))
	}

	// bare array form
	arr, _ := json.Marshal(payload.Items)
	res2, err := ImportJSON(arr)
	if err != nil {
		t.Fatalf("array form: %v", err)
	}
	if len(res2.Plan.Nodes) != 3 {
		t.Fatalf("array form nodes = %d", len(res2.Plan.Nodes))
	}
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
