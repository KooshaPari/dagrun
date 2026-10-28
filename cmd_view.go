package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/KooshaPari/dagrun/internal/export"
	"github.com/KooshaPari/dagrun/internal/harness"
	"github.com/KooshaPari/dagrun/internal/plan"
	"github.com/KooshaPari/dagrun/internal/trace"
)

func cmdStatus(args []string) error {
	var dir string
	var jsonOut bool
	flags, _ := splitArgs(args)
	fs := flagSet("status", &dir, &jsonOut)
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
	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(snap)
	}

	all := st.All(snap)
	counts := map[string]int{}
	for _, n := range all {
		if ns := snap.Nodes[n.ID]; ns != nil {
			counts[ns.Status]++
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "run %s   nodes %d\n", p.Run, len(all))
	// fixed order so output is diffable
	for _, s := range []string{plan.StatusDone, plan.StatusRunning, plan.StatusPending, plan.StatusFailed, plan.StatusSkipped, plan.StatusCancelled} {
		if counts[s] > 0 {
			fmt.Fprintf(&b, "  %-10s %d\n", s, counts[s])
		}
	}
	b.WriteString("\n")

	for _, n := range all {
		ns := snap.Nodes[n.ID]
		if ns == nil {
			continue
		}
		glyph := statusGlyph(ns.Status)
		extra := ""
		if ns.Attempts > 1 {
			extra = fmt.Sprintf(" attempt=%d", ns.Attempts)
		}
		if ns.Status == plan.StatusRunning && ns.Owner != "" {
			extra += " owner=" + ns.Owner
		}
		if ns.LastError != "" && (ns.Status == plan.StatusFailed || ns.Status == plan.StatusPending) {
			extra += " " + truncate(ns.LastError, 70)
		}
		arts := 0
		if ns.Verify != nil {
			for _, a := range ns.Verify.Artifacts {
				if a.OK {
					arts++
				}
			}
		}
		fmt.Fprintf(&b, "%s %-8s %-9s %-6s %d/%d %s%s\n",
			glyph, n.ID, n.Kind, n.Estimate, arts, len(n.Artifacts), n.Title, extra)
	}

	// Fan-out templates are structure, not work: exclude them from progress.
	done, total := counts[plan.StatusDone], 0
	for _, n := range all {
		if n.FanOut == "" {
			total++
		}
	}
	pct := 0
	if total > 0 {
		pct = done * 100 / total
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "progress %d/%d (%d%%)\n", done, total, pct)
	fmt.Print(b.String())
	return nil
}

func cmdTrace(args []string) error {
	var dir string
	var jsonOut bool
	flags, _ := splitArgs(args)
	fs := flagSet("trace", &dir, &jsonOut)
	if err := fs.Parse(flags); err != nil {
		return err
	}
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		return fmt.Errorf("no run directory at %s", dir)
	}
	events, err := trace.Merge(dir + "/trace")
	if err != nil {
		return err
	}
	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		for _, e := range events {
			if err := enc.Encode(e); err != nil {
				return err
			}
		}
		return nil
	}
	if len(events) == 0 {
		fmt.Println("no events yet")
		return nil
	}
	fmt.Print(trace.Render(events))
	return nil
}

func cmdExport(args []string) error {
	var dir string
	flags, _ := splitArgs(args)
	fs := flagSet("export", &dir, nil)
	out := fs.String("out", "", "write to file instead of stdout")
	format := fs.String("format", "json", "agileplus | tracera | json")
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
	events, err := trace.Merge(st.Dir() + "/trace")
	if err != nil {
		return err
	}
	in := export.Input{Plan: p, State: snap, Events: events}

	var data []byte
	switch strings.ToLower(*format) {
	case "agileplus", "agile-plus", "ap":
		data, err = export.AgilePlus(in)
	case "tracera", "trace":
		data, err = export.Tracera(in)
	case "json":
		data, err = export.JSON(in)
	default:
		return fmt.Errorf("unknown format %q; want agileplus, tracera, or json", *format)
	}
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if *out != "" {
		return os.WriteFile(*out, data, 0o644)
	}
	_, err = os.Stdout.Write(data)
	return err
}

func cmdDetect(args []string) error {
	var dir string
	var jsonOut bool
	flags, _ := splitArgs(args)
	fs := flagSet("detect", &dir, &jsonOut)
	if err := fs.Parse(flags); err != nil {
		return err
	}
	infos := harness.Detect()
	launching := harness.Launching()
	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{"launching": launching, "harnesses": infos})
	}
	fmt.Printf("launching harness: %s\n\n", orUnknown(launching))
	fmt.Printf("%-12s %-8s %-26s %s\n", "NAME", "STATE", "VERSION", "PATH")
	for _, i := range infos {
		state := "missing"
		if i.Installed {
			state = "ok"
		}
		fmt.Printf("%-12s %-8s %-26s %s\n", i.Name, state, truncate(i.Version, 26), i.Path)
		if i.Note != "" && i.Installed {
			fmt.Printf("%-12s note: %s\n", "", i.Note)
		}
	}
	return nil
}

func orUnknown(s string) string {
	if s == "" {
		return "(unknown)"
	}
	return s
}
