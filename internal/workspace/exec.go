// Package workspace runs one intent across several repositories.
//
// The scheduling decision belongs to the workspace: only it can see that a node
// in "web" depends on a node in "api". The execution machinery belongs to
// internal/run: worktree isolation, hooks, fan-out expansion, usage capture,
// failure classification and retries all live there.
//
// So the workspace runs one internal/run executor per repo, and each executor
// asks the workspace whether its node may start. That keeps a single
// implementation of the hard parts rather than two that drift.
package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/KooshaPari/dagrun/internal/plan"
	"github.com/KooshaPari/dagrun/internal/run"
	"github.com/KooshaPari/dagrun/internal/store"
	"github.com/KooshaPari/dagrun/internal/trace"
)

// repoRuntime is one repo's plan, store and trace within a workspace run.
type repoRuntime struct {
	repo  Repo
	root  string
	dir   string
	plan  *plan.Plan
	store *store.Store
	trace *trace.Writer
	exec  *run.Executor
}

// RunOptions configures a workspace run.
type RunOptions struct {
	// Workers is the GLOBAL cap across every repo, not a per-repo cap.
	Workers int
	Owner   string
	Lease   time.Duration
	DryRun  bool
	Verbose bool
	// ExecTemplate is the fallback command for nodes that name no harness.
	ExecTemplate string
	// Tools is the tool allow-list passed to each harness.
	Tools []string
	Out   interface{ Write([]byte) (int, error) }
	// RunDir is the per-repo run directory name, relative to each repo root.
	RunDir string
	// Reset discards existing progress in every repo.
	Reset bool
	// MaxCostUSD caps the spend across every repo.
	MaxCostUSD float64
	// Rate prices the tokens a node reports. When it is the zero Rate, each
	// repo's executor prices from the default rate table. Setting it here
	// prices the whole workspace the same way, which is what makes the shared
	// ceiling mean the same number everywhere.
	Rate plan.Rate
	// Once runs at most one node across the whole workspace.
	Once bool
}

// RunSummary is the outcome of a workspace run.
type RunSummary struct {
	Repos   int      `json:"repos"`
	Done    int      `json:"done"`
	Failed  int      `json:"failed"`
	Skipped int      `json:"skipped"`
	Pending int      `json:"pending"`
	Adopted int      `json:"adopted"`
	Ran     int      `json:"ran"`
	Nodes   []string `json:"nodes,omitempty"`
	// Remaining names the nodes that did not finish, namespaced.
	Remaining []string `json:"remaining,omitempty"`
	// CostUSD is the measured spend of the WHOLE workspace, and CostLimitUSD
	// is the ceiling it was held to. Both are zero when no ceiling was set.
	//
	// The spend is the aggregate across every repo, not a sum of per-repo
	// figures each measured against its own limit: the ceiling belongs to the
	// run, so the number reported belongs to the run too.
	CostUSD      float64 `json:"cost_usd,omitempty"`
	CostLimitUSD float64 `json:"cost_limit_usd,omitempty"`
}

// Run executes the workspace as one DAG.
//
// Each repo runs its own executor, so a repo's own plan remains the contract
// for its nodes. The workspace decides the ORDER: a node may not start until
// every dependency, in any repo, is terminal.
func (w *Workspace) Run(ctx context.Context, opt RunOptions) (RunSummary, error) {
	if opt.Workers < 1 {
		opt.Workers = 4
	}
	if opt.Owner == "" {
		opt.Owner = "workspace"
	}
	if opt.Lease <= 0 {
		opt.Lease = 2 * time.Minute
	}
	if opt.RunDir == "" {
		opt.RunDir = ".dagrun"
	}
	if opt.Out == nil {
		opt.Out = os.Stderr
	}

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

	// The cross-repo gate. A node in repo R may start only when every
	// dependency named in the COMBINED plan is terminal, wherever it lives.
	states := map[string]*store.State{}
	var statesMu sync.Mutex
	// refresh re-reads every repo's state. The gate must see the CURRENT state,
	// not the state at startup: the api repo's node finishes while the web repo's
	// executor is already polling, and a stale snapshot would keep the web node
	// pending forever.
	refresh := func() {
		statesMu.Lock()
		defer statesMu.Unlock()
		for root, rt := range repos {
			if st, err := rt.store.Snapshot(); err == nil {
				states[root] = st
			}
		}
	}

	// A node that has not been claimed yet is neither done nor failed. The gate
	// must treat "not there yet" as WAITABLE, or a repo whose executor has not
	// started would look like a permanently broken dependency.
	knownButUnfinished := func(root, local string) (status string, known bool) {
		st := states[root]
		if st == nil {
			return "", false
		}
		ns := st.Nodes[local]
		if ns == nil {
			return "", false
		}
		return ns.Status, true
	}

	// gate reports (ready, waitable) for one node of one repo.
	//
	// waitable is the whole difference between a wait and a hang. A dependency
	// that is merely not YET terminal may still be produced by another repo's
	// executor, so the caller waits. A dependency that is already terminal and
	// not done (failed or cancelled) can never become done, so no amount of
	// waiting will open this gate; reporting waitable=false is what lets the
	// executor fail the node instead of polling forever.
	//
	// refresh() takes statesMu and releases it before returning, so the second
	// Lock below is a fresh acquisition, not a re-entrant one.
	gate := func(ctx context.Context, ownerRoot, localID string) (bool, bool) {
		refresh()
		statesMu.Lock()
		defer statesMu.Unlock()
		for _, cn := range combined.Nodes {
			if cn == nil {
				continue
			}
			repo, local, ok := w.repoForNamespaced(cn.ID)
			if !ok || local != localID || w.RepoRoot(repo) != ownerRoot {
				continue
			}
			for _, d := range cn.Deps {
				drepo, dlocal, ok := w.repoForNamespaced(d)
				if !ok {
					return false, false // an unresolvable dep blocks for good
				}
				status, known := knownButUnfinished(w.RepoRoot(drepo), dlocal)
				if !known {
					// The dependency is not known in that repo's state yet.
					// It may not have been claimed, or it may be a fan_out
					// child that does not exist until expansion. Either way,
					// waiting is right and failing would be wrong.
					return false, true
				}
				switch status {
				case plan.StatusDone, plan.StatusSkipped:
					continue
				case plan.StatusFailed, plan.StatusCancelled:
					return false, false // terminal and not done: never opens
				}
				return false, true // still in flight somewhere: wait
			}
			return true, true
		}
		return true, true
	}

	// A per-repo gate that also consults the workspace gate.
	var workers int64
	global := make(chan struct{}, opt.Workers)

	// ONE budget for the whole workspace. Without this each repo's executor
	// would get the full ceiling, so a three-repo run with a 10 USD cap could
	// spend 30 USD while the plan said 10.
	var sharedBudget *run.SharedBudget
	if opt.MaxCostUSD > 0 {
		sharedBudget = run.NewSharedBudget(opt.MaxCostUSD)
	}

	var (
		wg       sync.WaitGroup
		errMu    sync.Mutex
		firstErr error
	)
	setErr := func(e error) {
		if e == nil {
			return
		}
		errMu.Lock()
		if firstErr == nil {
			firstErr = e
		}
		errMu.Unlock()
	}

	for _, rt := range repos {
		rt := rt
		repoGate := func(ctx context.Context, nodeID string) (bool, bool) {
			return gate(ctx, rt.root, nodeID)
		}
		// The global cap: a slot is held for as long as a node is in flight,
		// across every repo. Acquire and Release are paired by the executor.
		acquire := func() bool {
			select {
			case global <- struct{}{}:
				atomic.AddInt64(&workers, 1)
				return true
			default:
				return false
			}
		}
		release := func() {
			select {
			case <-global:
				atomic.AddInt64(&workers, -1)
			default:
			}
		}
		rt.exec = run.New(rt.store, rt.trace, run.Options{
			Workers:      1, // the workspace owns the global cap
			Owner:        opt.Owner,
			Lease:        opt.Lease,
			Once:         opt.Once,
			DryRun:       opt.DryRun,
			Verbose:      opt.Verbose,
			ExecTemplate: opt.ExecTemplate,
			Tools:        opt.Tools,
			Out:          opt.Out,
			MaxCostUSD:   opt.MaxCostUSD,
			Rate:         opt.Rate,
			// ONE budget for the whole workspace, so the ceiling is a property
			// of the run rather than of each repo.
			SharedBudget: sharedBudget,
			Ready:        repoGate,
			Acquire:      acquire,
			Release:      release,
		})
	}

	for _, rt := range repos {
		rt := rt
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := rt.exec.Loop(ctx); err != nil {
				setErr(err)
			}
		}()
	}
	wg.Wait()

	sum, _ := w.summarize(repos)
	sum.CostUSD, sum.CostLimitUSD = w.costReport(repos, sharedBudget)
	return sum, firstErr
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
		out[root] = &repoRuntime{
			repo:  r,
			root:  root,
			dir:   dir,
			plan:  p,
			store: st,
			trace: trace.New(filepath.Join(dir, "trace")),
		}
	}
	return out, nil
}

// summarize counts the outcome across every repo.
func (w *Workspace) summarize(repos map[string]*repoRuntime) (RunSummary, error) {
	sum := RunSummary{Repos: len(repos)}
	for _, rt := range repos {
		st, err := rt.store.Snapshot()
		if err != nil {
			return sum, err
		}
		for _, n := range rt.plan.Sorted() {
			ns := st.Nodes[n.ID]
			if ns == nil {
				continue
			}
			sum.Nodes = append(sum.Nodes, n.ID)
			switch ns.Status {
			case plan.StatusDone:
				sum.Done++
				if ns.Attempts == 0 {
					sum.Adopted++
				}
			case plan.StatusFailed:
				sum.Failed++
			case plan.StatusSkipped:
				sum.Skipped++
			case plan.StatusRunning:
				sum.Ran++
			default:
				sum.Pending++
			}
			if ns.Status == plan.StatusFailed || ns.Status == plan.StatusPending || ns.Status == plan.StatusRunning {
				sum.Remaining = append(sum.Remaining, w.Namespace(rt.repo, n.ID))
			}
		}
	}
	return sum, nil
}

// costReport returns the workspace's aggregate spend against its ceiling.
//
// The number is read from the shared budget, so it is the sum of what every
// repo actually spent. Reading it from one repo's executor would understate
// it by that repo's share.
func (w *Workspace) costReport(repos map[string]*repoRuntime, shared *run.SharedBudget) (spent, max float64) {
	if shared == nil {
		return 0, 0
	}
	return shared.Spent()
}
