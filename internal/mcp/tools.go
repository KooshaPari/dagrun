package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/KooshaPari/dagrun/internal/harness"
	"github.com/KooshaPari/dagrun/internal/plan"
	"github.com/KooshaPari/dagrun/internal/store"
	"github.com/KooshaPari/dagrun/internal/verify"
)

// tool is one MCP tool definition.
type tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func obj(props map[string]any, required ...string) map[string]any {
	schema := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func str(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func num(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

func boolean(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}

// toolList is the catalogue advertised to clients.
func toolList() []tool {
	dir := str("run directory holding run.yaml and state.json (default .dagrun)")
	node := str("node id, e.g. 1.2 or 4.1/auth-go")
	return []tool{
		{
			Name:        "dagrun_next",
			Description: "List nodes that are ready to run: dependencies done, artifacts not yet valid. Returns the generated prompt so a caller can hand it to an agent.",
			InputSchema: obj(map[string]any{"dir": dir, "role": str("only nodes with this role"), "count": num("limit the number returned")}),
		},
		{
			Name:        "dagrun_claim",
			Description: "Atomically take a node for an owner. Fails when the node is done, leased, blocked on dependencies, or a concurrency cap is reached.",
			InputSchema: obj(map[string]any{"dir": dir, "node": node, "owner": str("owner identity"), "ttl_seconds": num("lease length in seconds (default 120)")}, "node"),
		},
		{
			Name:        "dagrun_done",
			Description: "Verify a node's declared artifacts and mark it done. Returns isError when the contract is not met and force is false.",
			InputSchema: obj(map[string]any{"dir": dir, "node": node, "force": boolean("mark done even when artifacts do not verify")}, "node"),
		},
		{
			Name:        "dagrun_fail",
			Description: "Mark a node failed with a reason.",
			InputSchema: obj(map[string]any{"dir": dir, "node": node, "reason": str("why")}, "node"),
		},
		{
			Name:        "dagrun_skip",
			Description: "Mark a node skipped with a reason.",
			InputSchema: obj(map[string]any{"dir": dir, "node": node, "reason": str("why")}, "node"),
		},
		{
			Name:        "dagrun_status",
			Description: "Run status: per-status counts, progress, and every node with attempts, owner, and last error.",
			InputSchema: obj(map[string]any{"dir": dir}),
		},
		{
			Name:        "dagrun_validate",
			Description: "Re-check every node against the filesystem. Reports nodes recorded done that no longer verify, and nodes already satisfied that are not marked done.",
			InputSchema: obj(map[string]any{"dir": dir}),
		},
		{
			Name:        "dagrun_plan",
			Description: "Validate a plan file, install it as the run, and return the PERT summary (waves, critical path).",
			InputSchema: obj(map[string]any{"dir": dir, "file": str("plan file to install"), "reset": boolean("discard existing progress")}),
		},
		{
			Name:        "dagrun_explain",
			Description: "Diagnose one node: which predicate failed, why, and what to do next.",
			InputSchema: obj(map[string]any{"dir": dir, "node": node}, "node"),
		},
		{
			Name: "dagrun_trace",
			Description: "The merged execution trace: every event from every worker, in time order. " +
				"This is what happened, as opposed to what the state file says.",
			InputSchema: obj(map[string]any{"dir": dir, "limit": num("return only the last N events")}),
		},
		{
			Name: "dagrun_submit",
			Description: "Post the run to a Tracera instance at /ingest/agileplus. " +
				"Fails loud: a non-2xx status is an error, and so is a 2xx whose report carries " +
				"a non-empty error list. Use dry_run to get the exact payload without sending it.",
			InputSchema: obj(map[string]any{
				"dir":     dir,
				"url":     str("Tracera base URL, e.g. http://localhost:3180"),
				"token":   str("NAME of the env var holding the bearer token; never the token itself"),
				"format":  str("tracera (default) or agileplus"),
				"dry_run": boolean("return the payload without sending it"),
			}),
		},
		{
			Name: "dagrun_run",
			Description: "Execute ready nodes by spawning their harnesses, then return the summary. " +
				"BOUNDED: stops after max_nodes (default 1) so one call cannot hang a client. " +
				"An agent that wants to drive the whole run should loop next -> claim -> done " +
				"instead, so it owns each node rather than handing the run to the executor.",
			InputSchema: obj(map[string]any{
				"dir":       dir,
				"max_nodes": num("how many nodes to attempt before returning (default 1)"),
				"owner":     str("owner identity recorded on each claim"),
				"template":  str("shell command to run for nodes that name no harness"),
				"dry_run":   boolean("decide what would run without spawning anything"),
			}),
		},
	}
}

// runHandle is one opened run.
type runHandle struct {
	plan *plan.Plan
	st   *store.Store
	snap *store.State
}

func openRun(dir string) (*runHandle, error) {
	if strings.TrimSpace(dir) == "" {
		dir = ".dagrun"
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(abs, "run.yaml"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no plan installed at %s: call dagrun_plan first", filepath.Join(abs, "run.yaml"))
		}
		return nil, err
	}
	p, err := plan.Parse(raw)
	if err != nil {
		return nil, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	p.Root = cwd
	st, err := store.Open(abs, p, raw, false)
	if err != nil {
		return nil, err
	}
	snap, err := st.Snapshot()
	if err != nil {
		return nil, err
	}
	return &runHandle{plan: p, st: st, snap: snap}, nil
}

func (h *runHandle) find(id string) *plan.Node {
	for _, n := range h.st.All(h.snap) {
		if n != nil && n.ID == id {
			return n
		}
	}
	return nil
}

func (h *runHandle) index() map[string]*plan.Node {
	m := map[string]*plan.Node{}
	for _, n := range h.st.All(h.snap) {
		if n != nil {
			m[n.ID] = n
		}
	}
	return m
}

// callTool dispatches one tools/call.
func (s *Server) callTool(ctx context.Context, name string, raw json.RawMessage) (any, bool, error) {
	var in struct {
		Dir        string `json:"dir"`
		Node       string `json:"node"`
		Role       string `json:"role"`
		Owner      string `json:"owner"`
		Reason     string `json:"reason"`
		File       string `json:"file"`
		Count      int    `json:"count"`
		TTLSeconds int    `json:"ttl_seconds"`
		Force      bool   `json:"force"`
		Reset      bool   `json:"reset"`
		// trace
		Limit int `json:"limit"`
		// submit
		URL    string `json:"url"`
		Token  string `json:"token"`
		Format string `json:"format"`
		// run
		MaxNodes int    `json:"max_nodes"`
		Template string `json:"template"`
		DryRun   bool   `json:"dry_run"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, true, fmt.Errorf("invalid arguments: %w", err)
		}
	}
	switch name {
	case "dagrun_plan":
		return s.toolPlan(in.Dir, in.File, in.Reset)
	case "dagrun_next":
		return s.toolNext(ctx, in.Dir, in.Role, in.Count)
	case "dagrun_claim":
		return s.toolClaim(in.Dir, in.Node, in.Owner, in.TTLSeconds)
	case "dagrun_done":
		return s.toolDone(ctx, in.Dir, in.Node, in.Force)
	case "dagrun_fail", "dagrun_skip":
		return s.toolTerminal(in.Dir, in.Node, name, in.Reason)
	case "dagrun_status":
		return s.toolStatus(in.Dir)
	case "dagrun_validate":
		return s.toolValidate(ctx, in.Dir)
	case "dagrun_explain":
		return s.toolExplain(ctx, in.Dir, in.Node)
	case "dagrun_trace":
		return s.toolTrace(in.Dir, in.Limit)
	case "dagrun_submit":
		return s.toolSubmit(in.Dir, in.URL, in.Token, in.Format, in.DryRun)
	case "dagrun_run":
		return s.toolRun(ctx, in.Dir, in.Owner, in.Template, in.MaxNodes, in.DryRun)
	}
	return nil, true, fmt.Errorf("unknown tool %q", name)
}

func (s *Server) toolPlan(dir, file string, reset bool) (any, bool, error) {
	if strings.TrimSpace(file) == "" {
		return nil, true, fmt.Errorf("file is required")
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, true, err
	}
	p, err := plan.Parse(raw)
	if err != nil {
		return nil, true, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, true, err
	}
	p.Root = cwd
	if issues := p.Validate(); len(issues) > 0 {
		msgs := make([]string, 0, len(issues))
		for _, is := range issues {
			msgs = append(msgs, is.String())
		}
		return map[string]any{"ok": false, "issues": msgs}, true, nil
	}
	if strings.TrimSpace(dir) == "" {
		dir = ".dagrun"
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, true, err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, true, err
	}
	if err := os.WriteFile(filepath.Join(abs, "run.yaml"), raw, 0o644); err != nil {
		return nil, true, err
	}
	if _, err := store.Open(abs, p, raw, reset); err != nil {
		return nil, true, err
	}
	sched := p.Pert()
	return map[string]any{
		"ok":       true,
		"run":      p.Run,
		"dir":      abs,
		"nodes":    len(p.Nodes),
		"waves":    sched.Waves,
		"critical": sched.Critical,
	}, false, nil
}

func (s *Server) toolNext(ctx context.Context, dir, role string, count int) (any, bool, error) {
	h, err := openRun(dir)
	if err != nil {
		return nil, true, err
	}
	idx := h.index()
	type ready struct {
		ID       string `json:"id"`
		Kind     string `json:"kind"`
		Title    string `json:"title"`
		Role     string `json:"role"`
		Harness  string `json:"harness"`
		Resolved string `json:"resolved,omitempty"`
		Provider string `json:"provider,omitempty"`
		Model    string `json:"model,omitempty"`
		Prompt   string `json:"prompt"`
	}
	out := []ready{}
	for _, n := range h.st.All(h.snap) {
		if n == nil || n.FanOut != "" {
			continue
		}
		ns := h.snap.Nodes[n.ID]
		if ns == nil || ns.Status != plan.StatusPending {
			continue
		}
		if !h.st.DepsDone(h.snap, n.ID) {
			continue
		}
		if role != "" && n.Role != role {
			continue
		}
		resolved := ""
		if a, rerr := harness.Resolve(n.Harness); rerr == nil && a != nil {
			resolved = a.Name
		}
		out = append(out, ready{
			ID: n.ID, Kind: n.Kind, Title: n.Title, Role: n.Role,
			Harness: n.Harness, Resolved: resolved, Provider: n.Provider, Model: n.Model,
			Prompt: plan.Prompt(h.plan, n, idx),
		})
		if count > 0 && len(out) >= count {
			break
		}
	}
	return map[string]any{"run": h.plan.Run, "ready": out, "count": len(out)}, false, nil
}

func (s *Server) toolClaim(dir, node, owner string, ttl int) (any, bool, error) {
	h, err := openRun(dir)
	if err != nil {
		return nil, true, err
	}
	if owner == "" {
		host, _ := os.Hostname()
		owner = fmt.Sprintf("mcp.%s.%d", host, os.Getpid())
	}
	d := time.Duration(ttl) * time.Second
	if d <= 0 {
		d = 2 * time.Minute
	}
	c, err := h.st.Claim(node, owner, d, time.Now())
	if err != nil {
		return nil, true, err
	}
	return map[string]any{"node": node, "owner": owner, "won": c.Won, "epoch": c.Epoch, "reason": c.Reason}, !c.Won, nil
}

func (s *Server) toolDone(ctx context.Context, dir, node string, force bool) (any, bool, error) {
	h, err := openRun(dir)
	if err != nil {
		return nil, true, err
	}
	n := h.find(node)
	if n == nil {
		return nil, true, fmt.Errorf("unknown node %q", node)
	}
	rep := verify.PlanNode(ctx, h.st.Root(), n, h.plan)
	if !rep.OK && !force {
		return map[string]any{"node": node, "ok": false, "report": rep, "hint": verify.Hint(n, rep)}, true, nil
	}
	reason := ""
	if !rep.OK {
		reason = "forced done over MCP"
	}
	if err := h.st.Force(node, plan.StatusDone, reason, &rep, time.Now()); err != nil {
		return nil, true, err
	}
	return map[string]any{"node": node, "ok": true, "forced": !rep.OK, "report": rep}, false, nil
}

func (s *Server) toolTerminal(dir, node, tool, reason string) (any, bool, error) {
	h, err := openRun(dir)
	if err != nil {
		return nil, true, err
	}
	if h.find(node) == nil {
		return nil, true, fmt.Errorf("unknown node %q", node)
	}
	status := plan.StatusFailed
	if tool == "dagrun_skip" {
		status = plan.StatusSkipped
	}
	if err := h.st.Force(node, status, reason, nil, time.Now()); err != nil {
		return nil, true, err
	}
	return map[string]any{"node": node, "status": status}, false, nil
}

func (s *Server) toolStatus(dir string) (any, bool, error) {
	h, err := openRun(dir)
	if err != nil {
		return nil, true, err
	}
	type row struct {
		ID        string `json:"id"`
		Kind      string `json:"kind"`
		Status    string `json:"status"`
		Attempts  int    `json:"attempts"`
		Owner     string `json:"owner,omitempty"`
		Branch    string `json:"branch,omitempty"`
		LastError string `json:"last_error,omitempty"`
		Dynamic   bool   `json:"dynamic,omitempty"`
	}
	var rows []row
	counts := map[string]int{}
	total, settled, dynamic := 0, 0, 0
	for _, n := range h.st.All(h.snap) {
		if n == nil {
			continue
		}
		ns := h.snap.Nodes[n.ID]
		if ns == nil {
			continue
		}
		counts[ns.Status]++
		if n.FanOut == "" {
			total++
			if ns.Status == plan.StatusDone {
				settled++
			}
		}
		isDynamic := strings.Contains(n.ID, "/")
		if isDynamic {
			dynamic++
		}
		rows = append(rows, row{
			ID: n.ID, Kind: n.Kind, Status: ns.Status, Attempts: ns.Attempts,
			Owner: ns.Owner, Branch: ns.Branch, LastError: ns.LastError,
			Dynamic: isDynamic,
		})
	}
	progress := 0.0
	if total > 0 {
		progress = float64(settled) / float64(total)
	}
	return map[string]any{
		"run": h.plan.Run, "counts": counts, "progress": progress,
		"nodes": rows, "dynamic": dynamic,
	}, false, nil
}

func (s *Server) toolValidate(ctx context.Context, dir string) (any, bool, error) {
	h, err := openRun(dir)
	if err != nil {
		return nil, true, err
	}
	broken, adoptable := 0, 0
	var findings []map[string]string
	for _, n := range h.st.All(h.snap) {
		if n == nil {
			continue
		}
		ns := h.snap.Nodes[n.ID]
		if ns == nil {
			continue
		}
		rep := verify.PlanNode(ctx, h.st.Root(), n, h.plan)
		if ns.Status == plan.StatusDone && !rep.OK {
			broken++
			findings = append(findings, map[string]string{"node": n.ID, "reason": rep.Reason})
		}
		if ns.Status != plan.StatusDone && rep.OK {
			adoptable++
		}
	}
	return map[string]any{"ok": broken == 0, "broken": broken, "adoptable": adoptable, "findings": findings}, broken > 0, nil
}

func (s *Server) toolExplain(ctx context.Context, dir, node string) (any, bool, error) {
	h, err := openRun(dir)
	if err != nil {
		return nil, true, err
	}
	n := h.find(node)
	if n == nil {
		return nil, true, fmt.Errorf("unknown node %q", node)
	}
	ns := h.snap.Nodes[node]
	rep := verify.PlanNode(ctx, h.st.Root(), n, h.plan)
	status := plan.StatusPending
	attempts := 0
	lastErr := ""
	if ns != nil {
		status, attempts, lastErr = ns.Status, ns.Attempts, ns.LastError
	}
	blockedBy := []string{}
	for _, d := range n.Deps {
		if ds := h.snap.Nodes[d]; ds != nil && ds.Status != plan.StatusDone && ds.Status != plan.StatusSkipped {
			blockedBy = append(blockedBy, d+"="+ds.Status)
		}
	}
	return map[string]any{
		"node": node, "kind": n.Kind, "title": n.Title, "status": status,
		"attempts": attempts, "last_error": lastErr, "blocked_by": blockedBy,
		"ok": rep.OK, "findings": rep.Artifacts, "check": rep.Check,
		"verdict": rep.Reason, "hint": verify.Hint(n, rep),
	}, !rep.OK, nil
}
