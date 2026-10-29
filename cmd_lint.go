package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/KooshaPari/dagrun/internal/plan"
)

// cmdLint runs the semantic plan linter.
//
// Structural validation (cycles, unknown deps, bad paths) lives in
// plan.Validate; this adds the checks that catch a plan which is well-formed
// but wrong. Advisory findings are prefixed "warn:" and do not fail the command.
func cmdLint(args []string) error {
	fs := flag.NewFlagSet("lint", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dir := fs.String("dir", defaultDir, "run directory")
	file := fs.String("file", "", "plan file to lint (default: the installed run)")
	jsonOut := fs.Bool("json", false, "machine-readable output on stdout")
	if err := fs.Parse(args); err != nil {
		return err
	}

	var p *plan.Plan
	if *file != "" {
		raw, err := os.ReadFile(*file)
		if err != nil {
			return err
		}
		p, err = plan.Parse(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", *file, err)
		}
	} else {
		loaded, _, _, err := openRun(*dir)
		if err != nil {
			return err
		}
		p = loaded
	}

	issues := p.Validate()
	issues = append(issues, p.SemanticIssues()...)
	issues = append(issues, p.ScopeIssues()...)

	errors, warnings := 0, 0
	for _, is := range issues {
		if strings.HasPrefix(is.Code, "warn:") {
			warnings++
		} else {
			errors++
		}
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{
			"ok":       errors == 0,
			"issues":   issues,
			"errors":   errors,
			"warnings": warnings,
		})
	}
	if len(issues) == 0 {
		fmt.Println("no issues")
		return nil
	}
	for _, is := range issues {
		node := is.Node
		if node == "" {
			node = "-"
		}
		fmt.Printf("%-22s %-8s %s\n", is.Code, node, is.Detail)
	}
	fmt.Printf("\n%d error(s), %d warning(s)\n", errors, warnings)
	if errors > 0 {
		return fmt.Errorf("%d error(s)", errors)
	}
	return nil
}
