package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/KooshaPari/dagrun/internal/house"
	"github.com/KooshaPari/dagrun/internal/plan"
	"github.com/KooshaPari/dagrun/internal/run"
	"github.com/KooshaPari/dagrun/internal/trace"
)

func cmdRun(args []string) error {
	var dir string
	var jsonOut bool
	flags, _ := splitArgs(args)
	fs := flagSet("run", &dir, &jsonOut)
	workers := fs.Int("workers", 0, "concurrent nodes in this process")
	once := fs.Bool("once", false, "start at most one node then exit")
	dry := fs.Bool("dry-run", false, "report what would run, execute nothing")
	owner := fs.String("owner", "", "owner identity for leases")
	lease := fs.Duration("lease", 2*time.Minute, "lease duration")
	tmpl := fs.String("exec-template", "", "fallback command template")
	tools := fs.String("tools", "", "comma-separated tool allow-list")
	verbose := fs.Bool("verbose", false, "log spawns and child output paths")
	maxCost := fs.Float64("max-cost", 0, "cost ceiling in USD; 0 means the plan's own max_cost_usd, which may be 0 for no limit")
	forceRun := fs.Bool("force", false, "start even when a node names a harness that cannot run")
	if err := fs.Parse(flags); err != nil {
		return err
	}

	p, st, _, err := openRun(dir)
	if err != nil {
		return err
	}
	// Refuse a plan that cannot possibly run, BEFORE spending an attempt budget
	// per node discovering it. An unattended run has nobody watching to notice
	// the pattern, so this is the only moment where saying so helps. --force
	// exists because a harness may be installed mid-run by someone else.
	if !*forceRun && !*dry {
		var blockers []string
		for _, f := range house.HarnessFindings(p.Nodes) {
			if f.Actionable() {
				blockers = append(blockers, f.Detail+" ("+f.Action+")")
			}
		}
		if len(blockers) > 0 {
			return fmt.Errorf("cannot start: %s\n  fix the plan, install the harness, or pass --force",
				strings.Join(blockers, "\n  "))
		}
	}
	if *workers <= 0 {
		*workers = p.MaxParallel
	}
	if *workers <= 0 {
		*workers = 4
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	opt := run.Options{
		Workers:      *workers,
		Owner:        ownerOrDefault(*owner),
		Lease:        *lease,
		Once:         *once,
		DryRun:       *dry,
		Verbose:      *verbose,
		ExecTemplate: *tmpl,
		Tools:        splitList(*tools),
		Out:          os.Stderr,
		MaxCostUSD:   maxCostUSD(p, *maxCost),
	}
	// Per-provider admission control. The policy comes from the plan, so a
	// run's provider limits travel with the plan rather than living in a
	// shell history or an environment variable nobody can find.
	//
	// It is created even when the plan declares no policy, because a provider
	// THROTTLE gets a default backoff regardless: without it, a 429 is retried
	// immediately into the same wall and a transient throttle fails the run.
	// An empty policy still means "no cap, no cooldown" for every other
	// failure class.
	adm := plan.NewAdmission()
	adm.ApplyPolicy(p.ProviderPolicy)
	opt.Admission = adm
	opt.ProviderPolicy = p.ProviderPolicy
	tr := trace.New(st.Dir() + "/trace")
	ex := run.New(st, tr, opt)
	summary, runErr := ex.Loop(ctx)

	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(summary)
	} else {
		fmt.Printf("\nrun %s: done=%d failed=%d skipped=%d pending=%d\n",
			p.Run, summary.Done, summary.Failed, summary.Skipped, summary.Pending)
		if len(summary.Remaining) > 0 {
			fmt.Printf("remaining: %s\n", strings.Join(summary.Remaining, ", "))
		}
	}
	if runErr != nil {
		return runErr
	}
	if summary.Failed > 0 || summary.Pending > 0 {
		return fmt.Errorf("%d node(s) did not complete", summary.Failed+summary.Pending)
	}
	return nil
}

func ownerOrDefault(owner string) string {
	if owner != "" {
		return owner
	}
	host, _ := os.Hostname()
	if host == "" {
		host = "host"
	}
	return fmt.Sprintf("%s.%d.%d", host, os.Getpid(), time.Now().UnixNano()%1000)
}

func splitList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// maxCostUSD picks the cost ceiling: the flag wins, then the plan's own
// declaration, then no limit.
func maxCostUSD(p *plan.Plan, flagValue float64) float64 {
	if flagValue > 0 {
		return flagValue
	}
	if p != nil {
		return p.MaxCostUSD
	}
	return 0
}
