package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/KooshaPari/dagrun/internal/harness"
	"github.com/KooshaPari/dagrun/internal/plan"
	"github.com/KooshaPari/dagrun/internal/store"
	"github.com/KooshaPari/dagrun/internal/verify"
)

// readyNode is what an agent-driving loop consumes from `dagrun next`.
type readyNode struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	Title     string          `json:"title"`
	Role      string          `json:"role"`
	Harness   string          `json:"harness"`
	Resolved  string          `json:"resolved,omitempty"`
	Provider  string          `json:"provider,omitempty"`
	Model     string          `json:"model,omitempty"`
	Estimate  string          `json:"estimate"`
	Deps      []string        `json:"deps,omitempty"`
	Prompt    string          `json:"prompt"`
	Artifacts []plan.Artifact `json:"artifacts,omitempty"`
	Check     string          `json:"check,omitempty"`
	Runnable  bool            `json:"runnable_by_run"`
	Satisfied bool            `json:"artifacts_already_valid,omitempty"`

	// CVP awareness: how far this node sits from the checkpoint that closes the
	// run. DistanceToCVP counts dependency hops from the CVP gate down to this
	// node, and is -1 when the gate is unknown or this node is not on the
	// gate's work set. OnCVPPath means the gate transitively depends on it.
	DistanceToCVP int  `json:"distance_to_cvp"`
	OnCVPPath     bool `json:"on_cvp_path"`
}

// cvpChainNode is one prerequisite on the path to the CVP gate.
type cvpChainNode struct {
	ID            string `json:"id"`
	Kind          string `json:"kind"`
	Title         string `json:"title"`
	Status        string `json:"status"`
	DistanceToCVP int    `json:"distance_to_cvp"`
}

func cmdNext(args []string) error {
	var dir string
	var jsonOut bool
	flags, _ := splitArgs(args)
	fs := flagSet("next", &dir, &jsonOut)
	role := fs.String("role", "", "only nodes with this role")
	count := fs.Int("count", 0, "limit the number returned")
	byCVP := fs.Bool("by-cvp", false, "order ready nodes by proximity to the CVP gate")
	onlyCVP := fs.Bool("cvp", false, "print the CVP gate and the chain of nodes that must finish before it")
	if err := fs.Parse(flags); err != nil {
		return err
	}
	p, st, raw, err := openRun(dir)
	if err != nil {
		return err
	}
	snap, err := st.Snapshot()
	if err != nil {
		return err
	}
	gate := plan.ResolveCVPGate(p, raw)

	if *onlyCVP {
		return printCVP(p, snap, gate, jsonOut)
	}

	idx := p.Index()
	ctx := context.Background()
	var out []readyNode
	for _, n := range p.Sorted() {
		ns := snap.Nodes[n.ID]
		if ns == nil || ns.Status != plan.StatusPending {
			continue
		}
		if !st.DepsDone(snap, n.ID) {
			continue
		}
		if *role != "" && n.Role != *role {
			continue
		}
		resolved := ""
		if a, err := harness.Resolve(n.Harness); err == nil && a != nil {
			resolved = a.Name
		}
		dist, onPath := plan.CVPDistance(p, gate, n.ID)
		out = append(out, readyNode{
			ID: n.ID, Kind: n.Kind, Title: n.Title, Role: n.Role,
			Harness: n.Harness, Resolved: resolved, Provider: n.Provider, Model: n.Model,
			Estimate: n.Estimate, Deps: n.Deps,
			Prompt:    plan.Prompt(p, n, idx),
			Artifacts: n.Artifacts, Check: n.Check,
			Runnable:      len(n.Artifacts) > 0 || n.Check != "" || n.Prompt != "",
			Satisfied:     verify.Satisfied(ctx, st.Root(), n),
			DistanceToCVP: dist,
			OnCVPPath:     onPath,
		})
	}

	if *byCVP {
		orderByCVP(out)
	}
	if *count > 0 && len(out) > *count {
		out = out[:*count]
	}

	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{"run": p.Run, "cvp_gate": gate, "ready": out, "count": len(out)})
	}
	if len(out) == 0 {
		if gate != "" {
			fmt.Printf("no ready nodes (cvp gate %s)\n", gate)
			return nil
		}
		fmt.Println("no ready nodes")
		return nil
	}
	for _, r := range out {
		marker := " "
		if r.OnCVPPath {
			marker = "*"
		}
		suffix := ""
		if *byCVP || r.OnCVPPath {
			if r.OnCVPPath {
				suffix = fmt.Sprintf("  cvp-%d", r.DistanceToCVP)
			} else {
				suffix = "  off-cvp"
			}
		}
		fmt.Printf("%s%s  %-9s %-40s via %s (%s)%s\n",
			marker, r.ID, r.Kind, truncate(r.Title, 40), r.Harness, r.Resolved, suffix)
	}
	if gate != "" {
		fmt.Printf("\n%d ready node(s); cvp gate %s; use --json for prompts\n", len(out), gate)
	} else {
		fmt.Printf("\n%d ready node(s); use --json for prompts\n", len(out))
	}
	return nil
}

// statusOf reports a node's recorded status, or "unknown" when the run has no
// state for it yet.
func statusOf(snap *store.State, id string) string {
	if snap == nil || snap.Nodes == nil {
		return "unknown"
	}
	if ns := snap.Nodes[id]; ns != nil && ns.Status != "" {
		return ns.Status
	}
	return "unknown"
}

// orderByCVP sorts ready nodes so the work that closes the checkpoint comes
// first: nodes on the CVP path (the gate transitively depends on them) before
// everything else, then fewest hops from the gate, then WBS id for determinism.
func orderByCVP(nodes []readyNode) {
	sort.SliceStable(nodes, func(i, j int) bool {
		a, b := nodes[i], nodes[j]
		if a.OnCVPPath != b.OnCVPPath {
			return a.OnCVPPath
		}
		if a.OnCVPPath && a.DistanceToCVP != b.DistanceToCVP {
			return a.DistanceToCVP < b.DistanceToCVP
		}
		return plan.Less(a.ID, b.ID)
	})
}

// printCVP answers "what is the checkpoint, and what must happen first?" in
// topological order with each prerequisite's recorded status.
func printCVP(p *plan.Plan, snap *store.State, gate string, jsonOut bool) error {
	if gate == "" {
		return fmt.Errorf("no CVP gate: the plan declares no GATE node and no cvp_gate: key")
	}
	gateNode := p.ByID(gate)
	ancestors := plan.OnPathTo(p, gate)
	chain := make([]cvpChainNode, 0, len(ancestors))
	for _, id := range p.Topo() {
		if !ancestors[id] {
			continue
		}
		n := p.ByID(id)
		if n == nil {
			continue
		}
		dist, _ := plan.CVPDistance(p, gate, id)
		chain = append(chain, cvpChainNode{
			ID: n.ID, Kind: n.Kind, Title: n.Title,
			Status:        statusOf(snap, id),
			DistanceToCVP: dist,
		})
	}
	title := ""
	if gateNode != nil {
		title = gateNode.Title
	}
	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{
			"gate": gate, "title": title, "prereqs": chain, "count": len(chain),
		})
	}
	fmt.Printf("cvp gate: %s  %s\n", gate, title)
	if len(chain) == 0 {
		fmt.Println("no prerequisites; the gate is ready")
		return nil
	}
	fmt.Printf("\n%d node(s) must finish first (topological order):\n", len(chain))
	pending := 0
	for _, c := range chain {
		if c.Status != plan.StatusDone && c.Status != plan.StatusSkipped {
			pending++
		}
		fmt.Printf("  %-7s %-9s %-40s %s\n", c.ID, c.Kind, truncate(c.Title, 40), c.Status)
	}
	fmt.Printf("\n%d of %d still open\n", pending, len(chain))
	return nil
}
