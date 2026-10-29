package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/KooshaPari/dagrun/internal/plan"
	"github.com/KooshaPari/dagrun/internal/store"
	"github.com/KooshaPari/dagrun/internal/verify"
)

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
