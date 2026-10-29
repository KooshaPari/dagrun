package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/KooshaPari/dagrun/internal/timeline"
	"github.com/KooshaPari/dagrun/internal/trace"
)

// cmdTimeline renders the run timeline.
func cmdTimeline(args []string) error {
	fs := flag.NewFlagSet("timeline", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dir := fs.String("dir", defaultDir, "run directory")
	jsonOut := fs.Bool("json", false, "machine-readable output on stdout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	events, err := trace.Merge(filepath.Join(*dir, "trace"))
	if err != nil {
		return err
	}
	built, sum := timeline.Build(events)
	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{"events": built, "summary": sum})
	}
	fmt.Print(timeline.Render(built, sum))
	return nil
}
