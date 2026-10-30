package mcp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/KooshaPari/dagrun/internal/plan"
	"github.com/KooshaPari/dagrun/internal/run"
	"github.com/KooshaPari/dagrun/internal/trace"
	"github.com/KooshaPari/dagrun/internal/tracera"
)

// The three tools here close the gap that made dagrun awkward to drive from
// another agent: trace and submit were CLI-only, and there was no way to ask
// dagrun to execute anything. A pane had to drop to a shell, which is exactly
// the round trip the MCP surface exists to remove.

// toolTrace returns the merged execution trace.
//
// The trace is the run's evidence, and it is deliberately separate from the
// state file: state says where the run IS, the trace says how it got there.
// A caller debugging a run needs the second, and it was previously reachable
// only through `dagrun trace`.
func (s *Server) toolTrace(dir string, limit int) (any, bool, error) {
	h, err := openRun(dir)
	if err != nil {
		return nil, true, err
	}
	events, err := trace.Merge(filepath.Join(h.st.Dir(), "trace"))
	if err != nil {
		return nil, true, err
	}
	if limit > 0 && len(events) > limit {
		events = events[len(events)-limit:]
	}
	// A run with no events yet must report an empty ARRAY, not null: a client
	// that iterates the field should not have to special-case nil, and "null"
	// reads like the field is missing rather than empty.
	if events == nil {
		events = []trace.Event{}
	}
	return map[string]any{
		"count":  len(events),
		"events": events,
	}, false, nil
}

// toolSubmit posts the run to a Tracera instance.
//
// The token is named by ENV VAR, never passed by value. That matters more for
// MCP than for the CLI: a tool argument is written into the client's transcript
// and often into a log, so a literal token would leak by design. The env-var
// indirection keeps it out of both.
func (s *Server) toolSubmit(dir, url, tokenEnv, format string, dryRun bool) (any, bool, error) {
	h, err := openRun(dir)
	if err != nil {
		return nil, true, err
	}
	if strings.TrimSpace(format) == "" {
		format = tracera.FormatTracera
	}
	payload, err := tracera.BuildPayload(h.plan, h.snap, format)
	if err != nil {
		return nil, true, err
	}
	if dryRun {
		return map[string]any{"dry_run": true, "items": len(payload.Items), "payload": payload}, false, nil
	}
	if strings.TrimSpace(url) == "" {
		return nil, true, fmt.Errorf("url is required unless dry_run is set")
	}
	var token string
	if strings.TrimSpace(tokenEnv) != "" {
		token = os.Getenv(tokenEnv)
		if token == "" {
			return nil, true, fmt.Errorf("token names env var %s, which is empty or unset", tokenEnv)
		}
	}
	c, err := tracera.New(url, token)
	if err != nil {
		return nil, true, err
	}
	// A submission is a network call, so it gets its own bound rather than
	// inheriting whatever deadline the client set.
	ctx, cancel := context.WithTimeout(context.Background(), tracera.DefaultTimeout)
	defer cancel()
	res, err := c.Send(ctx, payload)
	if err != nil {
		return nil, true, err
	}
	return map[string]any{
		"items":        len(payload.Items),
		"processed":    res.TotalProcessed,
		"requirements": res.RequirementsCreated,
		"trace_links":  res.TraceLinksCreated,
		"url":          url,
	}, false, nil
}

// MaxRunNodes bounds one dagrun_run call. The default is deliberately 1: a
// tool call that spawned an unbounded executor would block the client until the
// whole run finished, which can be hours.
const MaxRunNodes = 50

// toolRun executes ready nodes and returns a summary.
//
// It is BOUNDED on purpose. The agent-driven loop (next -> claim -> done) is the
// better shape for an agent that wants to own each node, and the tool
// description says so. This tool exists for the other case: a caller that wants
// dagrun to do the dispatch itself and only needs the outcome.
func (s *Server) toolRun(ctx context.Context, dir, owner, template string, maxNodes int, dryRun bool) (any, bool, error) {
	h, err := openRun(dir)
	if err != nil {
		return nil, true, err
	}
	if maxNodes <= 0 {
		maxNodes = 1
	}
	if maxNodes > MaxRunNodes {
		return nil, true, fmt.Errorf("max_nodes %d exceeds the limit of %d; loop dagrun_next instead", maxNodes, MaxRunNodes)
	}
	if owner == "" {
		owner = "mcp"
	}
	// A run must not outlive the client's request by much: the caller is
	// waiting on this frame.
	runCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	opt := run.Options{
		Workers:        1,
		Owner:          owner,
		Lease:          2 * time.Minute,
		Once:           maxNodes == 1,
		DryRun:         dryRun,
		ExecTemplate:   template,
		Out:            s.log,
		MaxCostUSD:     h.plan.MaxCostUSD,
		ProviderPolicy: h.plan.ProviderPolicy,
	}
	// The plan's provider policy must become live admission state. Without
	// this the limits are parsed and then ignored, which is worse than absent:
	// the plan would claim a cap it never enforces.
	if len(h.plan.ProviderPolicy.Limits) > 0 {
		adm := plan.NewAdmission()
		adm.ApplyPolicy(h.plan.ProviderPolicy)
		opt.Admission = adm
	}

	ex := run.New(h.st, trace.New(filepath.Join(h.st.Dir(), "trace")), opt)
	sum, err := ex.Loop(runCtx)
	if err != nil {
		return nil, true, err
	}
	spent, budget := ex.BudgetSpent()
	return map[string]any{
		"summary":    sum,
		"cost_usd":   spent,
		"cost_limit": budget,
	}, false, nil
}
