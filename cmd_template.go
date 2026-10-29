package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/KooshaPari/dagrun/internal/plan"
)

// cmdTemplate prints a plan template.
//
// A template is the shortest path from "I have a job to do" to a plan that
// validates, so the default action with no arguments is the list: the names and
// what each one is for.
//
//	dagrun template --list
//	dagrun template greenfield
//	dagrun template bugfix --out plan.yaml
//
// The arguments are parsed here rather than through flagSet because --out is a
// command-local value flag and main.go's splitArgs does not know it.
func cmdTemplate(args []string) error {
	var list bool
	var out string
	var pos []string

	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			pos = append(pos, args[i+1:]...)
			i = len(args)
		case a == "--list" || a == "-list":
			list = true
		case a == "--out" || a == "-out":
			if i+1 >= len(args) {
				return fmt.Errorf("--out needs a file")
			}
			out = args[i+1]
			i++
		case strings.HasPrefix(a, "--out=") || strings.HasPrefix(a, "-out="):
			out = a[strings.IndexByte(a, '=')+1:]
		case a == "--help" || a == "-h" || a == "help":
			printTemplateList(os.Stdout)
			return nil
		case strings.HasPrefix(a, "-") && a != "-":
			return fmt.Errorf("unknown flag %q (try `dagrun template --list`)", a)
		default:
			pos = append(pos, a)
		}
	}

	// No name, or --list: show what is available.
	if list || len(pos) == 0 {
		if out != "" {
			return fmt.Errorf("--out needs a template name")
		}
		printTemplateList(os.Stdout)
		return nil
	}
	if len(pos) > 1 {
		return fmt.Errorf("one template at a time, got %d: %s", len(pos), strings.Join(pos, " "))
	}

	name := pos[0]
	if name == "-" {
		return fmt.Errorf("template name is required, not -")
	}
	raw, err := plan.Template(name)
	if err != nil {
		// plan.Template already names the alternatives.
		return err
	}

	if out == "" || out == "-" {
		_, err = os.Stdout.Write(raw)
		return err
	}
	if err := os.WriteFile(out, raw, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "wrote %s (%d bytes)\n", out, len(raw))
	return nil
}

// printTemplateList writes the name/description table.
func printTemplateList(w *os.File) {
	all := plan.Templates()
	if len(all) == 0 {
		fmt.Fprintln(w, "no templates are embedded in this build")
		return
	}
	width := 0
	for _, t := range all {
		if len(t.Name) > width {
			width = len(t.Name)
		}
	}
	fmt.Fprintln(w, "templates:")
	for _, t := range all {
		fmt.Fprintf(w, "  %-*s  %s\n", width, t.Name, t.Description)
	}
	fmt.Fprintln(w, "\nusage: dagrun template NAME [--out FILE]")
}
