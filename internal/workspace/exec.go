package workspace

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/KooshaPari/dagrun/internal/harness"
	"github.com/KooshaPari/dagrun/internal/plan"
	"github.com/KooshaPari/dagrun/internal/store"
	"github.com/KooshaPari/dagrun/internal/verify"
)

// DefaultRunDir is the per-repo run directory, relative to the repo root.
const DefaultRunDir = ".dagrun"

// repoRuntime is one repo's opened run, as the workspace executor sees it.
//
// The combined plan is the source of ORDER; this plan is the source of the
// node CONTRACT (prompt, artifacts, check, timeout) and this root is what its
// artifact paths resolve against.
type repoRuntime struct {
	repo  Repo
	root  string
	dir   string
	plan  *plan.Plan
	store *store.Store
}

// repoForNamespaced resolves a namespaced id ("web/2.1") to its owning repo and
// the local id inside that repo ("2.1").
func (w *Workspace) repoForNamespaced(id string) (Repo, string, bool) {
	for _, r := range w.Repos {
		prefix := w.Prefix(r) + "/"
		if strings.HasPrefix(id, prefix) {
			return r, strings.TrimPrefix(id, prefix), true
		}
	}
	return Repo{}, "", false
}

// NodeOwner resolves a namespaced id to the repo that owns it and the node as
// that repo's own plan declares it.
//
// The returned repoPath is the repo root, which is what the node's artifact
// paths resolve against. The node is the local node of record, never the
// synthetic combined-plan copy, so a scheduler can execute the real contract.
func (w *Workspace) NodeOwner(combined *plan.Plan, id string) (repoPath string, node *plan.Node, ok bool) {
	if combined != nil {
		known := false
		for _, n := range combined.Nodes {
			if n != nil && n.ID == id {
				known = true
				break
			}
		}
		if !known {
			return "", nil, false
		}
	}
	repo, local, found := w.repoForNamespaced(id)
	if !found {
		return "", nil, false
	}
	p, err := plan.Load(w.PlanPath(repo))
	if err != nil {
		return "", nil, false
	}
	n := p.ByID(local)
	if n == nil {
		return "", nil, false
	}
	return w.RepoRoot(repo), n, true
}

// ReadyNodes returns the namespaced ids that may start now.
//
// states is keyed by absolute repo root and holds each repo's own run state.
// A node is ready only when it is pending in its own store AND every dependency
// the COMBINED plan names is done or skipped, in whichever repo owns it. That
// last clause is the whole point: a node in "web" cannot start while the "api"
// node it depends on is unfinished.
func (w *Workspace) ReadyNodes(combined *plan.Plan, states map[string]*store.State) []string {
	var out []string
	if combined == nil {
		return out
	}
	for _, n := range combined.Sorted() {
		if n == nil || n.FanOut != "" {
			continue // a fan_out template is executed as its children
		}
		repo, local, ok := w.repoForNamespaced(n.ID)
		if !ok {
			continue
		}
		st := states[w.RepoRoot(repo)]
		if st == nil {
			continue
		}
		ns := st.Nodes[local]
		if ns == nil || ns.Status != plan.StatusPending {
			continue
		}
		if !depsDoneInWorkspace(w, n, states) {
			continue
		}
		out = append(out, n.ID)
	}
	return out
}

// depsDoneInWorkspace reports whether every combined dependency is terminal.
// An unresolvable or unreadable dependency is NOT done: a missing dependency
// blocks rather than being silently treated as satisfied.
func depsDoneInWorkspace(w *Workspace, n *plan.Node, states map[string]*store.State) bool {
	for _, d := range n.Deps {
		repo, local, ok := w.repoForNamespaced(d)
		if !ok {
			return false
		}
		st := states[w.RepoRoot(repo)]
		if st == nil {
			return false
		}
		ds := st.Nodes[local]
		if ds == nil {
			return false
		}
		if ds.Status != plan.StatusDone && ds.Status != plan.StatusSkipped {
			return false
		}
	}
	return true
}

// RunOptions configures a workspace run.
type RunOptions struct {
	Workers      int
	Owner        string
	Lease        time.Duration
	DryRun       bool
	Verbose      bool
	ExecTemplate string
	// Tools is the tool allow-list passed to each harness.
	Tools []string
	Out   io.Writer
	// RunDir is the per-repo run directory name, relative to each repo root.
	RunDir string
	// Reset discards existing progress in every repo.
	Reset bool
}

func (o *RunOptions) normalize() {
	if o.Workers < 1 {
		o.Workers = 1
	}
	if o.Lease <= 0 {
		o.Lease = 2 * time.Minute
	}
	if o.Out == nil {
		o.Out = os.Stderr
	}
	if o.RunDir == "" {
		o.RunDir = DefaultRunDir
	}
	if o.Owner == "" {
		host, _ := os.Hostname()
		if host == "" {
			host = "host"
		}
		o.Owner = fmt.Sprintf("%s.%d", host, os.Getpid())
	}
}

// RunSummary is the outcome of a workspace run.
type RunSummary struct {
	Repos     int      `json:"repos"`
	Nodes     int      `json:"nodes"`
	Done      int      `json:"done"`
	Failed    int      `json:"failed"`
	Skipped   int      `json:"skipped"`
	Pending   int      `json:"pending"`
	Ran       int      `json:"ran"`
	Remaining []string `json:"remaining,omitempty"`
}

// Run executes one workspace as one DAG.
//
// Order comes from the combined plan. Each node is claimed, executed, and
// finished in its OWN repo's store, using that repo's plan and root, so every
// repo keeps its own state. The worker cap is global, not per repo.
func (w *Workspace) Run(ctx context.Context, opt RunOptions) (RunSummary, error) {
	opt.normalize()
	combined, _, err := w.CombinedPlan()
	if err != nil {
		return RunSummary{}, err
	}
	order := combined.Topo()
	if len(order) != len(combined.Nodes) {
		return RunSummary{}, fmt.Errorf("combined plan has a dependency cycle: ordered %d of %d nodes",
			len(order), len(combined.Nodes))
	}

	repos, err := w.openRepos(opt)
	if err != nil {
		return RunSummary{}, err
	}

	var (
		active  int64
		wg      sync.WaitGroup
		errMu   sync.Mutex
		firstEr error
	)
	setErr := func(e error) {
		if e == nil {
			return
		}
		errMu.Lock()
		if firstEr == nil {
			firstEr = e
		}
		errMu.Unlock()
	}

	for {
		if ctx.Err() != nil {
			break
		}
		states, serr := snapshotAll(w, repos)
		if serr != nil {
			return RunSummary{}, serr
		}
		launched := 0
		for _, id := range w.ReadyNodes(combined, states) {
			if atomic.LoadInt64(&active) >= int64(opt.Workers) {
				break
			}
			repo, local, ok := w.repoForNamespaced(id)
			if !ok {
				continue
			}
			rt := repos[w.RepoRoot(repo)]
			if rt == nil {
				continue
			}
			claim, cerr := rt.store.Claim(local, opt.Owner, opt.Lease, time.Now())
			if cerr != nil {
				setErr(cerr)
				continue
			}
			if !claim.Won {
				continue // another worker won it, or this repo's cap is full
			}
			atomic.AddInt64(&active, 1)
			launched++
			wg.Add(1)
			go func(rt *repoRuntime, localID, namespaced string, epoch int64) {
				defer wg.Done()
				defer atomic.AddInt64(&active, -1)
				if e := w.executeNode(ctx, rt, localID, namespaced, epoch, opt); e != nil {
					setErr(e)
				}
			}(rt, local, id, claim.Epoch)
			if opt.Workers == 1 {
				break
			}
		}
		if launched == 0 && atomic.LoadInt64(&active) == 0 {
			break // nothing running, nothing startable
		}
		select {
		case <-ctx.Done():
		case <-time.After(100 * time.Millisecond):
		}
	}

	wg.Wait()
	sum, _ := w.summarize(repos)
	return sum, firstEr
}

// openRepos opens (and installs, if needed) the per-repo run directory.
func (w *Workspace) openRepos(opt RunOptions) (map[string]*repoRuntime, error) {
	out := make(map[string]*repoRuntime, len(w.Repos))
	for _, r := range w.Repos {
		root := w.RepoRoot(r)
		dir := filepath.Join(root, opt.RunDir)
		installed := filepath.Join(dir, "run.yaml")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
		var raw []byte
		if b, err := os.ReadFile(installed); err == nil {
			raw = b
		} else {
			b, rerr := os.ReadFile(w.PlanPath(r))
			if rerr != nil {
				return nil, rerr
			}
			raw = b
			// Install the plan of record so the run's plan hash is stable across
			// invocations and matches what `dagrun plan` would have written.
			if werr := os.WriteFile(installed, raw, 0o644); werr != nil {
				return nil, werr
			}
		}
		p, perr := plan.Parse(raw)
		if perr != nil {
			return nil, fmt.Errorf("%s: %w", installed, perr)
		}
		p.Root = root
		st, serr := store.Open(dir, p, raw, opt.Reset)
		if serr != nil {
			return nil, fmt.Errorf("%s: %w", r.Path, serr)
		}
		out[root] = &repoRuntime{repo: r, root: root, dir: dir, plan: p, store: st}
	}
	return out, nil
}

func snapshotAll(w *Workspace, repos map[string]*repoRuntime) (map[string]*store.State, error) {
	states := make(map[string]*store.State, len(repos))
	for root, rt := range repos {
		st, err := rt.store.Snapshot()
		if err != nil {
			return nil, err
		}
		states[root] = st
	}
	return states, nil
}

func (w *Workspace) summarize(repos map[string]*repoRuntime) (RunSummary, error) {
	sum := RunSummary{Repos: len(repos)}
	for _, rt := range repos {
		st, err := rt.store.Snapshot()
		if err != nil {
			return sum, err
		}
		for _, n := range rt.plan.Nodes {
			if n == nil || n.FanOut != "" {
				continue
			}
			sum.Nodes++
			ns := st.Nodes[n.ID]
			if ns == nil {
				continue
			}
			switch ns.Status {
			case plan.StatusDone:
				sum.Done++
			case plan.StatusFailed:
				sum.Failed++
			case plan.StatusSkipped:
				sum.Skipped++
			case plan.StatusCancelled:
				sum.Skipped++
			default:
				sum.Pending++
				sum.Remaining = append(sum.Remaining, w.Namespace(rt.repo, n.ID))
			}
		}
	}
	return sum, nil
}

// executeNode runs one node against its owning repo. It is the workspace's own
// minimal executor: claim happened in the caller, finish happens here.
func (w *Workspace) executeNode(ctx context.Context, rt *repoRuntime, localID, namespaced string, epoch int64, opt RunOptions) error {
	start := time.Now()
	n := rt.plan.ByID(localID)
	if n == nil {
		return fmt.Errorf("%s: node vanished from %s", namespaced, rt.plan.Run)
	}
	workdir := rt.root
	if n.Workdir != "" {
		workdir = filepath.Join(rt.root, n.Workdir)
	}

	if opt.DryRun {
		fmt.Fprintf(opt.Out, "would run %s (%s) in %s\n", namespaced, n.Kind, rt.root)
		_, err := rt.store.Finish(localID, opt.Owner, epoch, plan.StatusSkipped, nil, 0, "dry run", time.Now())
		return err
	}

	// A check-only node has no child: its evidence already exists on disk.
	if n.Check != "" && len(n.Artifacts) == 0 {
		rep := verify.PlanNode(ctx, rt.root, n, rt.plan)
		return w.settle(rt, n, namespaced, epoch, rep, 0, start, opt)
	}

	prompt := plan.Prompt(rt.plan, n, rt.plan.Index())
	name, args, err := w.command(n, prompt, workdir, opt)
	if err != nil {
		return w.settle(rt, n, namespaced, epoch,
			verify.Report{NodeID: n.ID, Reason: err.Error()}, -1, start, opt)
	}

	logDir := filepath.Join(rt.dir, "logs", plan.Slug(localID))
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return err
	}
	logPath := filepath.Join(logDir, fmt.Sprintf("%d.log", time.Now().UnixNano()))
	logFile, err := os.Create(logPath)
	if err != nil {
		return err
	}
	defer logFile.Close()

	cctx, cancel := context.WithTimeout(ctx, n.TimeoutDuration())
	defer cancel()
	cmd := exec.CommandContext(cctx, name, args...)
	cmd.Dir = workdir
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = os.Environ()
	if opt.Verbose {
		fmt.Fprintf(opt.Out, "spawn %s -> %s %s\n", namespaced, name, strings.Join(args, " "))
	}

	runErr := cmd.Run()
	exitCode := 0
	reason := ""
	if runErr != nil {
		if ee, ok := runErr.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			exitCode = -1
			reason = runErr.Error()
		}
	}

	rep := verify.PlanNode(ctx, rt.root, n, rt.plan)
	if reason != "" && rep.OK {
		// The binary never ran, so nothing it claims can be trusted.
		rep.OK = false
		rep.Reason = reason
	}
	return w.settle(rt, n, namespaced, epoch, rep, exitCode, start, opt)
}

// command picks the executable for a node: harness when named, else the
// template. There is no worktree isolation here; a workspace node runs in its
// own repo checkout.
func (w *Workspace) command(n *plan.Node, prompt, workdir string, opt RunOptions) (string, []string, error) {
	explicit := opt.ExecTemplate != "" && (n.Harness == "exec" || n.Harness == "auto")
	if !explicit && n.Harness != "exec" {
		a, err := harness.Resolve(n.Harness)
		if err == nil && a != nil {
			path, args, cerr := a.Command(harness.Spec{
				Prompt:   prompt,
				Workdir:  workdir,
				Provider: n.Provider,
				Model:    n.Model,
				Tools:    opt.Tools,
			})
			return path, args, cerr
		}
		if err != nil && opt.ExecTemplate == "" {
			return "", nil, err
		}
	}
	if opt.ExecTemplate == "" {
		return "", nil, fmt.Errorf("node %s needs harness exec but no exec template is configured", n.ID)
	}
	name, args, _, err := harness.ExecCommand(opt.ExecTemplate, prompt, workdir, n.Model)
	return name, args, err
}

// settle records the outcome in the owning repo's store. A failed node is
// retried only while its attempt budget allows.
func (w *Workspace) settle(rt *repoRuntime, n *plan.Node, namespaced string, epoch int64, rep verify.Report, exitCode int, start time.Time, opt RunOptions) error {
	status := plan.StatusDone
	reason := ""
	if !rep.OK {
		reason = rep.Reason
		status = plan.StatusFailed
		if n.MaxAttempts > 1 {
			status = plan.StatusPending
		}
	}
	ok, err := rt.store.Finish(n.ID, opt.Owner, epoch, status, &rep, exitCode, reason, time.Now())
	if err != nil {
		return err
	}
	if !ok {
		fmt.Fprintf(opt.Out, "lost lease on %s; result discarded\n", namespaced)
		return nil
	}
	switch status {
	case plan.StatusDone:
		fmt.Fprintf(opt.Out, "done %s (%s) %dms\n", namespaced, n.Kind, time.Since(start).Milliseconds())
	case plan.StatusFailed:
		fmt.Fprintf(opt.Out, "fail %s (%s): %s\n", namespaced, n.Kind, reason)
	default:
		fmt.Fprintf(opt.Out, "retry %s (%s): %s\n", namespaced, n.Kind, reason)
	}
	return nil
}
