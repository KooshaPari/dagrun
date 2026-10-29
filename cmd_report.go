package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/KooshaPari/dagrun/internal/plan"
	"github.com/KooshaPari/dagrun/internal/report"
	"github.com/KooshaPari/dagrun/internal/trace"
)

// cmdCritique writes a critique of a plan.
func cmdCritique(args []string) error {
	fs := flag.NewFlagSet("critique", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dir := fs.String("dir", defaultDir, "run directory")
	file := fs.String("file", "", "plan file to critique (default: the installed run)")
	out := fs.String("out", "", "write here (default CRITIQUE.md)")
	jsonOut := fs.Bool("json", false, "machine-readable output on stdout")
	if err := fs.Parse(args); err != nil {
		return err
	}

	p, err := loadPlan(*dir, *file)
	if err != nil {
		return err
	}
	c := report.CritiquePlan(p)

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(c)
	}
	text := report.RenderCritique(c)
	if *out == "" {
		fmt.Print(text)
		return nil
	}
	return os.WriteFile(*out, []byte(text), 0o644)
}

// cmdReview writes a review of a run.
func cmdReview(args []string) error {
	fs := flag.NewFlagSet("review", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dir := fs.String("dir", defaultDir, "run directory")
	out := fs.String("out", "", "write here (default REVIEW.md)")
	jsonOut := fs.Bool("json", false, "machine-readable output on stdout")
	if err := fs.Parse(args); err != nil {
		return err
	}

	p, st, _, err := openRun(*dir)
	if err != nil {
		return err
	}
	snap, err := st.Snapshot()
	if err != nil {
		return err
	}
	events, err := mergeTrace(st.Dir())
	if err != nil {
		return err
	}
	r := report.ReviewRun(p, snap, events)

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	}
	text := report.RenderReview(r)
	if *out == "" {
		fmt.Print(text)
		return nil
	}
	return os.WriteFile(*out, []byte(text), 0o644)
}

// loadPlan reads a plan from a file, or from the installed run.
func loadPlan(dir, file string) (*plan.Plan, error) {
	if file != "" {
		raw, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		p, err := plan.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		return p, nil
	}
	p, _, _, err := openRun(dir)
	return p, err
}

// mergeTrace reads the merged trace of a run directory.
func mergeTrace(runDir string) ([]trace.Event, error) {
	return trace.Merge(filepath.Join(runDir, "trace"))
}
