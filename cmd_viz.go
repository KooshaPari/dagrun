package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/KooshaPari/dagrun/internal/viz"
)

// cmdViz renders the run as a self-contained interactive HTML viewer.
//
// The output is one file: canvas renderer, embedded JSON, no network. Note the
// flags are parsed directly rather than through splitArgs, because --out takes a
// value and viz accepts no positionals.
func cmdViz(args []string) error {
	if hasHelp(args) {
		fmt.Fprint(os.Stdout, `usage: dagrun viz [--dir DIR] [--out FILE] [--open] [--json]

Render the current run as one self-contained interactive HTML file.

  --dir DIR    run directory (default .dagrun)
  --out FILE   output path (default viz.html in the current directory)
  --open       open the file with the OS default handler (darwin only)
  --json       print the document as JSON instead of writing HTML
`)
		return nil
	}
	var dir string
	var jsonOut bool
	fs := flagSet("viz", &dir, &jsonOut)
	out := fs.String("out", "viz.html", "output HTML file")
	openIt := fs.Bool("open", false, "open the rendered file (darwin only)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	doc, err := viz.Build(dir)
	if err != nil {
		return err
	}

	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(doc)
	}

	if err := viz.WriteFile(doc, *out); err != nil {
		return err
	}
	abs, absErr := filepath.Abs(*out)
	if absErr != nil {
		abs = *out
	}
	fmt.Printf("wrote %s\n  run %s   nodes %d   edges %d   progress %d%%\n",
		abs, doc.Run, len(doc.Nodes), len(doc.Edges), int(doc.Stats.Progress*100+0.5))

	if *openIt {
		openVizFile(abs)
	}
	return nil
}

// hasHelp reports whether -h/--help was asked for. The flag package would print
// its own terse usage; viz has flags of its own, so it prints a fuller one.
func hasHelp(args []string) bool {
	for _, a := range args {
		switch a {
		case "-h", "--h", "-help", "--help":
			return true
		}
	}
	return false
}

// openVizFile hands the file to the desktop on darwin and does nothing anywhere
// else: on a headless Linux box there is no handler to invoke.
func openVizFile(path string) {
	if runtime.GOOS != "darwin" {
		return
	}
	_ = exec.Command("open", path).Start()
}
