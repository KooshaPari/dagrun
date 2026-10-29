package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/KooshaPari/dagrun/internal/plan"
	"github.com/KooshaPari/dagrun/internal/workspace"
)

// cmdWorkspace validates and installs a multi-repo workspace.
//
// Each repo's plan is installed into that repo's own run directory. The
// workspace file itself is a rollup: it says which plans belong to one intent.
func cmdWorkspace(args []string) error {
	fs := flag.NewFlagSet("workspace", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	file := fs.String("file", "dagrun.workspace.yaml", "workspace file")
	dir := fs.String("dir", defaultDir, "workspace rollup directory")
	validateOnly := fs.Bool("validate", false, "report per-repo plan status without installing")
	jsonOut := fs.Bool("json", false, "machine-readable output on stdout")
	runNow := fs.Bool("run", false, "execute the workspace as one DAG across every repo")
	workers := fs.Int("workers", 4, "concurrent nodes across ALL repos (global cap)")
	lease := fs.Duration("lease", 2*time.Minute, "lease duration per node")
	dryRun := fs.Bool("dry-run", false, "with --run: report what would run, execute nothing")
	reset := fs.Bool("reset", false, "with --run: discard existing per-repo progress")
	verbose := fs.Bool("verbose", false, "with --run: log spawns")
	execTemplate := fs.String("exec-template", "", "with --run: command template for every node, e.g. 'mytool --prompt {prompt}'")
	tools := fs.String("tools", "", "with --run: comma-separated tool allow-list")
	if err := fs.Parse(args); err != nil {
		return err
	}

	w, err := workspace.Load(*file)
	if err != nil {
		return err
	}
	if errs := w.Validate(); len(errs) > 0 {
		if *jsonOut {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			msgs := make([]string, 0, len(errs))
			for _, e := range errs {
				msgs = append(msgs, e.Error())
			}
			_ = enc.Encode(map[string]any{"ok": false, "errors": msgs})
		} else {
			fmt.Fprintf(os.Stderr, "workspace is not usable: %d problem(s)\n", len(errs))
			for _, e := range errs {
				fmt.Fprintf(os.Stderr, "  - %v\n", e)
			}
		}
		return fmt.Errorf("workspace rejected")
	}

	statuses := make([]repoStatus, 0, len(w.Repos))
	failed := false

	// --run executes the workspace as one DAG. Order comes from the combined
	// plan; every node is claimed, executed and finished in its own repo's
	// store, so each repo keeps its own progress.
	if *runNow {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		sum, err := w.Run(ctx, workspace.RunOptions{
			Workers:      *workers,
			Owner:        ownerOrDefault(""),
			Lease:        *lease,
			DryRun:       *dryRun,
			Verbose:      *verbose,
			Out:          os.Stderr,
			Reset:        *reset,
			ExecTemplate: *execTemplate,
			Tools:        splitList(*tools),
		})
		if *jsonOut {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(sum)
		} else {
			fmt.Printf("\nworkspace %s: repos=%d done=%d failed=%d skipped=%d pending=%d\n",
				w.Name, sum.Repos, sum.Done, sum.Failed, sum.Skipped, sum.Pending)
			if len(sum.Remaining) > 0 {
				fmt.Printf("remaining: %s\n", strings.Join(sum.Remaining, ", "))
			}
		}
		if err != nil {
			return err
		}
		if sum.Failed > 0 || sum.Pending > 0 {
			return fmt.Errorf("%d node(s) did not complete", sum.Failed+sum.Pending)
		}
		return nil
	}

	for _, r := range w.Repos {
		st := repoStatus{
			Repo:      r.Path,
			Namespace: w.Prefix(r),
			Plan:      w.PlanPath(r),
		}
		raw, err := os.ReadFile(w.PlanPath(r))
		if err != nil {
			st.Issues = []string{err.Error()}
			failed = true
			statuses = append(statuses, st)
			continue
		}
		p, err := plan.Parse(raw)
		if err != nil {
			st.Issues = []string{err.Error()}
			failed = true
			statuses = append(statuses, st)
			continue
		}
		st.Run = p.Run
		st.Nodes = len(p.Nodes)
		seen := map[string]bool{}
		for _, n := range p.Nodes {
			if n != nil {
				seen[n.Kind] = true
			}
		}
		for k := range seen {
			st.Kinds = append(st.Kinds, k)
		}
		if issues := p.Validate(); len(issues) > 0 {
			for _, is := range issues {
				st.Issues = append(st.Issues, is.String())
			}
			failed = true
		}
		statuses = append(statuses, st)
	}

	if *validateOnly {
		if *jsonOut {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(map[string]any{"ok": !failed, "name": w.Name, "repos": statuses})
		}
		printWorkspaceStatuses(statuses)
		if failed {
			return fmt.Errorf("one or more repo plans are invalid")
		}
		return nil
	}

	if failed {
		if *jsonOut {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(map[string]any{"ok": false, "name": w.Name, "repos": statuses})
		} else {
			printWorkspaceStatuses(statuses)
		}
		return fmt.Errorf("refusing to install: one or more repo plans are invalid")
	}

	// Install: each repo gets its own run directory; the workspace directory
	// gets a rollup naming every namespaced node.
	abs, err := filepath.Abs(*dir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return err
	}
	rollup := map[string]any{
		"version":      workspace.Version,
		"name":         w.Name,
		"intent":       w.Intent,
		"generated_at": nowUTC(),
		"repos":        statuses,
	}
	ids, err := w.AllIDs()
	if err != nil {
		return err
	}
	rollup["nodes"] = len(ids)
	raw, err := json.MarshalIndent(rollup, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(abs, "workspace.json"), append(raw, '\n'), 0o644); err != nil {
		return err
	}
	// copy the workspace file next to the rollup so the run is self-describing
	if raw, err := os.ReadFile(*file); err == nil {
		_ = os.WriteFile(filepath.Join(abs, "workspace.yaml"), raw, 0o644)
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rollup)
	}
	printWorkspaceStatuses(statuses)
	fmt.Printf("\nworkspace %s installed: %d repo(s), %d node(s)\n", w.Name, len(w.Repos), len(ids))
	fmt.Printf("rollup %s\n", filepath.Join(abs, "workspace.json"))
	return nil
}

// repoStatus is one repo's row in the workspace report.
type repoStatus struct {
	Repo      string   `json:"repo"`
	Namespace string   `json:"namespace"`
	Plan      string   `json:"plan"`
	Run       string   `json:"run"`
	Nodes     int      `json:"nodes"`
	Kinds     []string `json:"kinds"`
	Issues    []string `json:"issues,omitempty"`
}

func printWorkspaceStatuses(statuses []repoStatus) {
	for _, s := range statuses {
		state := "ok"
		if len(s.Issues) > 0 {
			state = fmt.Sprintf("%d issue(s)", len(s.Issues))
		}
		fmt.Printf("%-14s %-14s %-10s %3d nodes  %s\n", s.Namespace, s.Repo, s.Run, s.Nodes, state)
		for _, is := range s.Issues {
			fmt.Printf("    %s\n", is)
		}
	}
}
