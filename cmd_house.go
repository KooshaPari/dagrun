package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/KooshaPari/dagrun/internal/house"
	"github.com/KooshaPari/dagrun/internal/store"
)

// cmdDoctor reports whether a run directory is healthy. It never mutates it.
func cmdDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dir := fs.String("dir", defaultDir, "run directory")
	jsonOut := fs.Bool("json", false, "machine-readable output on stdout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	abs, err := filepath.Abs(*dir)
	if err != nil {
		return err
	}
	var st *store.Store
	var snap *store.State
	if _, err := os.Stat(filepath.Join(abs, "run.yaml")); err == nil {
		if _, s, _, oerr := openRun(*dir); oerr == nil {
			st = s
			snap, _ = s.Snapshot()
		}
	}
	rep := house.Doctor(abs, st, snap)
	if snap == nil {
		rep.Findings = append(rep.Findings, house.Finding{
			Code: "unreadable_run", Severity: house.SeverityError,
			Detail: "the run directory could not be opened", Action: "run `dagrun plan FILE`",
		})
	}
	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}
	fmt.Printf("run %s  dir %s\n", orDash(rep.Run), rep.Dir)
	for _, c := range []string{"done", "running", "pending", "failed", "skipped"} {
		if rep.Counts[c] > 0 {
			fmt.Printf("  %-9s %d\n", c, rep.Counts[c])
		}
	}
	fmt.Println()
	if len(rep.Findings) == 0 {
		fmt.Println("no findings")
		return nil
	}
	for _, f := range rep.Findings {
		fmt.Printf("[%s] %-16s %s\n", f.Severity, f.Code, f.Detail)
		if f.Action != "" {
			fmt.Printf("                  -> %s\n", f.Action)
		}
	}
	if !rep.Healthy() {
		return fmt.Errorf("%d actionable finding(s)", countActionable(rep))
	}
	return nil
}

func countActionable(rep *house.Report) int {
	n := 0
	for _, f := range rep.Findings {
		if f.Actionable() {
			n++
		}
	}
	return n
}

// cmdGC prunes reclaimable run data. --dry-run changes nothing.
func cmdGC(args []string) error {
	fs := flag.NewFlagSet("gc", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dir := fs.String("dir", defaultDir, "run directory")
	older := fs.Duration("older-than", 72*time.Hour, "minimum age for logs and trace shards")
	dry := fs.Bool("dry-run", false, "report what would be removed, change nothing")
	jsonOut := fs.Bool("json", false, "machine-readable output on stdout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	abs, err := filepath.Abs(*dir)
	if err != nil {
		return err
	}
	var snap *store.State
	if _, s, _, oerr := openRun(*dir); oerr == nil {
		snap, _ = s.Snapshot()
	}
	if snap == nil {
		return fmt.Errorf("cannot read the run at %s", abs)
	}
	res := house.GC(abs, snap, *older, *dry)
	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	}
	verb := "removed"
	if *dry {
		verb = "would remove"
	}
	fmt.Printf("gc %s  dir %s\n", verb, abs)
	fmt.Printf("  files   %s (%d bytes)\n", verb, res.BytesFreed)
	for _, f := range res.FilesFreed {
		fmt.Printf("    %s\n", f)
	}
	for _, w := range res.Worktrees {
		fmt.Printf("    worktree %s\n", w)
	}
	if len(res.Kept) > 0 {
		fmt.Printf("  kept    %d\n", len(res.Kept))
		for _, r := range res.Reasons {
			fmt.Printf("    %s\n", r)
		}
	}
	return nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
