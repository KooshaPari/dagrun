package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

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
}

func cmdNext(args []string) error {
	var dir string
	var jsonOut bool
	flags, _ := splitArgs(args)
	fs := flagSet("next", &dir, &jsonOut)
	role := fs.String("role", "", "only nodes with this role")
	count := fs.Int("count", 0, "limit the number returned")
	if err := fs.Parse(flags); err != nil {
		return err
	}
	p, st, _, err := openRun(dir)
	if err != nil {
		return err
	}
	snap, err := st.Snapshot()
	if err != nil {
		return err
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
		out = append(out, readyNode{
			ID: n.ID, Kind: n.Kind, Title: n.Title, Role: n.Role,
			Harness: n.Harness, Resolved: resolved, Provider: n.Provider, Model: n.Model,
			Estimate: n.Estimate, Deps: n.Deps,
			Prompt:    plan.Prompt(p, n, idx),
			Artifacts: n.Artifacts, Check: n.Check,
			Runnable:  len(n.Artifacts) > 0 || n.Check != "" || n.Prompt != "",
			Satisfied: verify.Satisfied(ctx, st.Root(), n),
		})
		if *count > 0 && len(out) >= *count {
			break
		}
	}

	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{"run": p.Run, "ready": out, "count": len(out)})
	}
	if len(out) == 0 {
		fmt.Println("no ready nodes")
		return nil
	}
	for _, r := range out {
		fmt.Printf("%s  %-9s %-40s via %s (%s)\n", r.ID, r.Kind, truncate(r.Title, 40), r.Harness, r.Resolved)
	}
	fmt.Printf("\n%d ready node(s); use --json for prompts\n", len(out))
	return nil
}

func cmdClaim(args []string) error {
	var dir string
	var jsonOut bool
	flags, pos := splitArgs(args)
	fs := flagSet("claim", &dir, &jsonOut)
	owner := fs.String("owner", "", "owner identity")
	ttl := fs.Duration("ttl", 2*time.Minute, "lease duration")
	if err := fs.Parse(flags); err != nil {
		return err
	}
	if len(pos) == 0 {
		return fmt.Errorf("usage: dagrun claim NODE")
	}
	_, st, _, err := openRun(dir)
	if err != nil {
		return err
	}
	who := ownerOrDefault(*owner)
	claim, err := st.Claim(pos[0], who, *ttl, time.Now())
	if err != nil {
		return err
	}
	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{"node": pos[0], "owner": who, "won": claim.Won, "epoch": claim.Epoch, "reason": claim.Reason})
	}
	if !claim.Won {
		return fmt.Errorf("not claimed: %s", claim.Reason)
	}
	fmt.Printf("claimed %s owner=%s epoch=%d\n", pos[0], who, claim.Epoch)
	return nil
}

func cmdDone(args []string) error {
	var dir string
	var jsonOut bool
	flags, pos := splitArgs(args)
	fs := flagSet("done", &dir, &jsonOut)
	force := fs.Bool("force", false, "mark done even if artifacts do not verify")
	if err := fs.Parse(flags); err != nil {
		return err
	}
	if len(pos) == 0 {
		return fmt.Errorf("usage: dagrun done NODE")
	}
	p, st, _, err := openRun(dir)
	if err != nil {
		return err
	}
	n := findNode(p, pos[0])
	if n == nil {
		return fmt.Errorf("unknown node %q", pos[0])
	}
	rep := verify.Node(context.Background(), st.Root(), n)
	if !rep.OK && !*force {
		if jsonOut {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(rep)
		} else {
			fmt.Fprintf(os.Stderr, "%s does not satisfy its contract: %s\n", n.ID, rep.Reason)
		}
		return fmt.Errorf("verification failed")
	}
	reason := ""
	if !rep.OK {
		reason = "forced done by operator"
	}
	if err := st.Force(n.ID, plan.StatusDone, reason, &rep, time.Now()); err != nil {
		return err
	}
	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}
	fmt.Printf("done %s (%s)\n", n.ID, n.Kind)
	return nil
}

func cmdFail(args []string) error { return cmdTerminal(args, plan.StatusFailed, "fail") }
func cmdSkip(args []string) error { return cmdTerminal(args, plan.StatusSkipped, "skip") }

func cmdTerminal(args []string, status, name string) error {
	var dir string
	flags, pos := splitArgs(args)
	fs := flagSet(name, &dir, nil)
	reason := fs.String("reason", "", "why")
	if err := fs.Parse(flags); err != nil {
		return err
	}
	if len(pos) == 0 {
		return fmt.Errorf("usage: dagrun %s NODE [--reason TEXT]", name)
	}
	p, st, _, err := openRun(dir)
	if err != nil {
		return err
	}
	if findNode(p, pos[0]) == nil {
		return fmt.Errorf("unknown node %q", pos[0])
	}
	if err := st.Force(pos[0], status, *reason, nil, time.Now()); err != nil {
		return err
	}
	fmt.Printf("%s %s\n", status, pos[0])
	return nil
}

func cmdValidate(args []string) error {
	var dir string
	var jsonOut bool
	flags, _ := splitArgs(args)
	fs := flagSet("validate", &dir, &jsonOut)
	all := fs.Bool("all", false, "also report nodes that are not done")
	if err := fs.Parse(flags); err != nil {
		return err
	}
	p, st, _, err := openRun(dir)
	if err != nil {
		return err
	}
	snap, err := st.Snapshot()
	if err != nil {
		return err
	}
	ctx := context.Background()
	reports := make([]verify.Report, 0, len(p.Nodes))
	bad := 0
	adoptable := 0
	for _, n := range p.Sorted() {
		ns := snap.Nodes[n.ID]
		if ns == nil {
			continue
		}
		rep := verify.Node(ctx, st.Root(), n)
		if ns.Status == plan.StatusDone && !rep.OK {
			bad++
			reports = append(reports, rep)
			continue
		}
		if ns.Status != plan.StatusDone && rep.OK {
			adoptable++
		}
		if *all {
			reports = append(reports, rep)
		}
	}
	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{"ok": bad == 0, "broken": bad, "adoptable": adoptable, "reports": reports})
	}
	for _, r := range reports {
		state := "ok"
		if !r.OK {
			state = "BROKEN: " + r.Reason
		}
		fmt.Printf("%-8s %s\n", r.NodeID, state)
	}
	fmt.Printf("\n%d broken, %d already satisfied but not marked done\n", bad, adoptable)
	if bad > 0 {
		return fmt.Errorf("%d node(s) recorded done no longer verify", bad)
	}
	return nil
}

func findNode(p *plan.Plan, id string) *plan.Node {
	for _, n := range p.Nodes {
		if n != nil && n.ID == id {
			return n
		}
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n < 4 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

// statusGlyph maps a node status to one character for the tree view.
func statusGlyph(s string) string {
	switch s {
	case plan.StatusDone:
		return "+"
	case plan.StatusRunning:
		return ">"
	case plan.StatusFailed:
		return "x"
	case plan.StatusSkipped:
		return "-"
	case plan.StatusCancelled:
		return "c"
	}
	return "."
}

var _ = store.StateVersion
