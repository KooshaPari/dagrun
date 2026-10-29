// Package run is the executor: pick ready nodes, claim them, drive a child
// agent, verify the artifacts, and record the outcome.
//
// Correctness rests on two rules. A node is only "done" when its artifacts
// verify, and only the current lease holder with the current epoch may write a
// terminal state. Everything else is scheduling.
package run

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/KooshaPari/dagrun/internal/plan"
	"github.com/KooshaPari/dagrun/internal/store"
	"github.com/KooshaPari/dagrun/internal/trace"
	"github.com/KooshaPari/dagrun/internal/verify"
)

// Options configures one executor run.
type Options struct {
	Workers      int
	Owner        string
	Lease        time.Duration
	Once         bool
	DryRun       bool
	Verbose      bool
	ExecTemplate string
	Tools        []string
	Out          io.Writer

	// Ready, when set, is an EXTRA readiness condition on top of the node's own
	// dependencies. It is how a multi-repo run is scheduled: the workspace
	// decides that a node may not start until a node in ANOTHER repo is done,
	// and the per-repo executor asks.
	//
	// It returns (ready, waitable). ready means the node may start now.
	// waitable means the gate could still open later, so a not-ready node must
	// be waited on. A gate that reports waitable=false has proved the node can
	// NEVER become ready (its external dependency is terminal and not done), so
	// waiting would spin forever; the executor fails the node instead.
	//
	// A nil Ready means "the node's own dependencies are the whole story",
	// which is the single-repo behaviour and the default.
	Ready func(ctx context.Context, nodeID string) (ready bool, waitable bool)

	// Acquire reserves a worker slot before a node is claimed, and Release
	// returns it when the node finishes. Together they enforce a cap that
	// spans several executors, which a per-executor cap cannot do.
	//
	// Acquire returns false when there is no capacity: the node is simply not
	// started this tick.
	Acquire func() bool
	Release func()

	// MaxCostUSD caps the run's spend. Zero means no limit. The executor
	// stops claiming new nodes once the budget is spent, so an unattended run
	// has a ceiling rather than a hope.
	MaxCostUSD float64
	// Rate prices the tokens a node reports. When it is the zero Rate, the
	// default rate table is used.
	Rate plan.Rate
}

// externalBlockedReason is recorded on a node whose cross-repo dependency is
// terminal and not done. Waiting for such a dependency cannot make progress.
const externalBlockedReason = "blocked: a dependency in another repo is terminal and not done"

// Summary is the outcome of a run loop.
type Summary struct {
	Done      int      `json:"done"`
	Failed    int      `json:"failed"`
	Skipped   int      `json:"skipped"`
	Pending   int      `json:"pending"`
	Adopted   int      `json:"adopted"`
	Ran       int      `json:"ran"`
	Remaining []string `json:"remaining,omitempty"`
}

// Executor holds the per-process state of a run loop.
type Executor struct {
	st       *store.Store
	tr       *trace.Writer
	opt      Options
	active   int64
	wg       sync.WaitGroup
	errMu    sync.Mutex
	firstErr error

	// budget tracks the run's spend so it can be enforced, not just reported.
	budgetMu sync.Mutex
	budget   plan.BudgetState
}

// New builds an executor.
func New(st *store.Store, tr *trace.Writer, opt Options) *Executor {
	if opt.Workers < 1 {
		opt.Workers = 1
	}
	if opt.Lease <= 0 {
		opt.Lease = 2 * time.Minute
	}
	if opt.Out == nil {
		opt.Out = os.Stderr
	}
	return &Executor{st: st, tr: tr, opt: opt, budget: plan.NewBudget(opt.MaxCostUSD)}
}

// Loop schedules work until every node is terminal or nothing can progress.
func (e *Executor) Loop(ctx context.Context) (Summary, error) {
	_ = e.tr.Emit(e.opt.Owner, trace.Event{Run: e.st.Plan().Run, Worker: e.opt.Owner, Event: "run.start"})

	for {
		if ctx.Err() != nil {
			break
		}
		if reaped, err := e.st.ResetExpired(time.Now()); err == nil && len(reaped) > 0 {
			for _, id := range reaped {
				_ = e.tr.Emit(e.opt.Owner, trace.Event{Run: e.st.Plan().Run, Worker: e.opt.Owner, Node: id, Event: "lease.reap"})
			}
		}
		snap, err := e.st.Snapshot()
		if err != nil {
			return Summary{}, err
		}

		// A discover node that has verified turns its work set into real nodes.
		// Expansion is idempotent per parent, so several executors can call it.
		// Always re-snapshot afterwards: expansion mutates the run even when it
		// materialises zero children (an expanded marker, a skipped template).
		if _, _, xerr := e.expand(ctx, snap); xerr != nil {
			e.setErr(xerr)
		} else if fresh, serr := e.st.Snapshot(); serr == nil {
			snap = fresh
		}
		idx := indexAll(e.st, snap)

		launched := 0
		// capacityHeld records that a READY node could not start only because no
		// worker slot was free (the cap spans executors). That is not a reason
		// to conclude the run: the slot is released when the other in-flight
		// node finishes, so the executor must keep ticking.
		capacityHeld := false
		for _, n := range e.st.All(snap) {
			if atomic.LoadInt64(&e.active) >= int64(e.opt.Workers) {
				break
			}
			if n.FanOut != "" {
				continue // a template is executed as its children, never itself
			}
			ns := snap.Nodes[n.ID]
			if ns == nil || ns.Status != plan.StatusPending {
				continue
			}
			if n.FanIn != "" && !store.Expanded(snap, n.FanIn) {
				continue // the reduce cannot run before the map has expanded
			}
			// The external gate is consulted FIRST. A dependency that lives in
			// another repo is not in this repo's store, so DepsDone would be
			// false for a reason the local executor cannot resolve, and the
			// node would be skipped forever without the gate ever being asked.
			if e.opt.Ready != nil {
				ready, waitable := e.opt.Ready(ctx, n.ID)
				if !ready {
					if !waitable {
						// The gate is closed for good. Failing the node here is
						// what stops the loop from waiting on a state that no
						// executor will ever produce.
						e.finishDirect(n, plan.StatusFailed, externalBlockedReason)
					}
					continue // an external gate (another repo) is not satisfied yet
				}
			}
			if !e.st.DepsDone(snap, n.ID) {
				continue
			}
			if e.adopt(ctx, n) {
				launched++
				continue
			}
			if !runnable(n) {
				e.finishDirect(n, plan.StatusSkipped, "no execution path: no prompt, no check, no harness")
				continue
			}
			if e.opt.DryRun {
				fmt.Fprintf(e.opt.Out, "would run %s (%s) via %s\n", n.ID, n.Kind, harnessName(n))
				e.finishDirect(n, plan.StatusSkipped, "dry run")
				continue
			}
			// The budget is a ceiling: once it is spent, no NEW node is
			// claimed. Nodes already running finish and report, so the run
			// ends with a truthful state rather than a truncated one.
			if e.budgetSpent() {
				continue
			}
			// Reserve a worker slot before claiming. The slot is held for the
			// lifetime of the node, so the cap counts work in flight rather
			// than work started.
			if e.opt.Acquire != nil && !e.opt.Acquire() {
				capacityHeld = true
				continue // no capacity right now; try again next tick
			}
			claim, cerr := e.st.Claim(n.ID, e.opt.Owner, e.opt.Lease, time.Now())
			if cerr != nil {
				e.setErr(cerr)
				e.releaseSlot()
				continue
			}
			if !claim.Won {
				e.releaseSlot() // another process won it
				continue
			}
			atomic.AddInt64(&e.active, 1)
			launched++
			e.wg.Add(1)
			go func(node *plan.Node, epoch int64) {
				defer e.wg.Done()
				defer atomic.AddInt64(&e.active, -1)
				defer e.releaseSlot()
				e.execute(ctx, node, epoch, idx)
			}(n, claim.Epoch)
			_ = e.tr.Emit(e.opt.Owner, trace.Event{Run: e.st.Plan().Run, Worker: e.opt.Owner, Node: n.ID, Event: "node.claim", Epoch: claim.Epoch})
			if e.opt.Once {
				break
			}
		}

		running := atomic.LoadInt64(&e.active)
		if launched == 0 && running == 0 {
			// This snapshot and the expansion above were taken BEFORE the last
			// worker could write its terminal state (a worker may finish
			// between the snapshot and this check). A discover node that went
			// "done" in that window has never had its work set parsed, so
			// breaking here would end the run with a malformed work set still
			// reported as a green node. Re-expand against a fresh snapshot and
			// only conclude once expansion has nothing left to do.
			if changed, xerr := e.expandFresh(ctx); xerr != nil {
				e.setErr(xerr)
			} else if changed {
				continue
			}
			// Nothing running here and nothing new to start. Settle the rest.
			// A change means a node just became resolvable, and that can
			// unblock another node (a fan_in reduce, a workspace dependent), so
			// re-tick instead of concluding the run on the same snapshot.
			if e.settle(ctx) {
				continue
			}
			// Classify what the remaining nodes are waiting on. There are three
			// distinct answers and conflating them is what made this loop both
			// hang and drop work:
			//
			//   startable - the node is ready NOW but this tick passed it (the
			//               gate opened after the launch scan). Ticking again
			//               starts it.
			//   waiting   - an outside executor may still make it ready. Wait.
			//   permanent - an outside dependency is terminal and not done, so
			//               it can never be ready. settle() fails it; exiting
			//               here instead would drop the node silently.
			startable, waiting, permanent := e.externalGate(snap)
			if permanent {
				if e.settle(ctx) {
					continue
				}
				// The permanent nodes could not be written. Do not spin: they
				// stay pending and the summary reports them.
				if !startable && !waiting && !capacityHeld {
					break
				}
			}
			if startable || waiting || permanent || capacityHeld {
				select {
				case <-ctx.Done():
				case <-time.After(100 * time.Millisecond):
				}
				continue
			}
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(150 * time.Millisecond):
		}
	}

	e.wg.Wait()
	sum := e.summarize()
	_ = e.tr.Emit(e.opt.Owner, trace.Event{Run: e.st.Plan().Run, Worker: e.opt.Owner, Event: "run.end",
		Message: fmt.Sprintf("done=%d failed=%d skipped=%d pending=%d", sum.Done, sum.Failed, sum.Skipped, sum.Pending)})
	return sum, e.firstErr
}

// budgetSpent reports whether the run's cost ceiling has been crossed.
func (e *Executor) budgetSpent() bool {
	e.budgetMu.Lock()
	defer e.budgetMu.Unlock()
	return e.budget.Exceeded()
}

// BudgetSpent reports the run's spend against its ceiling.
func (e *Executor) BudgetSpent() (spent, max float64) {
	e.budgetMu.Lock()
	defer e.budgetMu.Unlock()
	return e.budget.Spent, e.budget.Max
}

// releaseSlot returns a reserved worker slot, if one was reserved.
func (e *Executor) releaseSlot() {
	if e.opt.Release != nil {
		e.opt.Release()
	}
}

// externalGate classifies the pending nodes that are ready apart from the
// external gate.
//
// startable means such a node is ready right now: the executor still has work
// to start, so it must tick again rather than exit.
//
// waiting means the gate could still open, so the executor must wait rather
// than exit: the gate belongs to another executor, which is what will unblock
// it.
//
// permanent means the gate can NEVER open, because an outside dependency is
// terminal and not done. settle() must fail such a node; exiting here instead
// is the silent-drop bug that left nodes pending with no failure reason.
func (e *Executor) externalGate(snap *store.State) (startable, waiting, permanent bool) {
	if e.opt.Ready == nil {
		return false, false, false
	}
	for _, n := range e.st.All(snap) {
		if n == nil || n.FanOut != "" {
			continue
		}
		ns := snap.Nodes[n.ID]
		if ns == nil || ns.Status != plan.StatusPending {
			continue
		}
		if !e.st.DepsDone(snap, n.ID) {
			continue // blocked by its own deps, not by the external gate
		}
		ready, waitable := e.opt.Ready(context.Background(), n.ID)
		if ready {
			startable = true
			continue
		}
		if !waitable {
			permanent = true
			continue
		}
		waiting = true
	}
	return startable, waiting, permanent
}

// settle marks unreachable nodes so the run terminates with a clear reason
// instead of spinning forever.
func (e *Executor) settle(ctx context.Context) bool {
	snap, err := e.st.Snapshot()
	if err != nil {
		return true
	}
	changed := false
	for _, n := range e.st.All(snap) {
		if n.FanOut != "" {
			continue
		}
		ns := snap.Nodes[n.ID]
		if ns == nil || ns.Status != plan.StatusPending {
			continue
		}
		if n.FanIn != "" && !store.Expanded(snap, n.FanIn) {
			// The discovery parent never produced a work set.
			switch ps := snap.Nodes[n.FanIn]; {
			case ps == nil:
				e.finishDirect(n, plan.StatusFailed, "fan_in parent "+n.FanIn+" is not in this run")
			case ps.Status == plan.StatusFailed || ps.Status == plan.StatusCancelled:
				e.finishDirect(n, plan.StatusFailed, "fan_in parent "+n.FanIn+" is "+ps.Status)
			case ps.Status == plan.StatusDone:
				e.finishDirect(n, plan.StatusSkipped, "work set is empty")
			}
			changed = true
			continue
		}
		if e.opt.Ready != nil {
			ready, waitable := e.opt.Ready(ctx, n.ID)
			if !ready {
				if waitable {
					// Waiting on another repo, not failed. Leave it pending.
					continue
				}
				// The external gate can never open. Fail the node instead of
				// waiting for a state no executor can produce.
				e.finishDirect(n, plan.StatusFailed, externalBlockedReason)
				changed = true
				continue
			}
		}
		if e.adopt(ctx, n) {
			changed = true
			continue
		}
		if !e.st.DepsDone(snap, n.ID) {
			// The dependency may live in ANOTHER repo, in which case this
			// executor cannot see it and must not declare the node blocked.
			// The external gate is the authority there.
			if _, waiting, _ := e.externalGate(snap); waiting {
				continue // still waiting on the other repo
			}
			var missing []string
			for _, d := range n.Deps {
				if ds := snap.Nodes[d]; ds != nil && ds.Status != plan.StatusDone && ds.Status != plan.StatusSkipped {
					missing = append(missing, d+"="+ds.Status)
				}
			}
			e.finishDirect(n, plan.StatusFailed, "blocked on "+strings.Join(missing, ", "))
			changed = true
			continue
		}
		if !runnable(n) {
			e.finishDirect(n, plan.StatusSkipped, "no execution path")
			changed = true
		}
	}
	return changed
}

func runnable(n *plan.Node) bool {
	if n.Check != "" || n.Prompt != "" {
		return true
	}
	return len(n.Artifacts) > 0 // dagrun generates the prompt from the contract
}

func harnessName(n *plan.Node) string {
	if n.Check != "" && len(n.Artifacts) == 0 {
		return "check"
	}
	if n.Harness == "" {
		return "auto"
	}
	return n.Harness
}

// adopt accepts a node whose artifacts already satisfy the contract. This is
// the duplicate-work guard: re-running the tool never redoes finished nodes.
func (e *Executor) adopt(ctx context.Context, n *plan.Node) bool {
	root := e.st.Root()
	if n.Isolation == IsolationWorktree {
		// Only adopt an isolated node if its worktree already exists; otherwise
		// there is nothing on disk to prove and it must actually run.
		wt := filepath.Join(e.st.Dir(), "worktrees", trace.Name(n.ID))
		if !isWorktree(wt) {
			return false
		}
		root = wt
	}
	if !verify.Satisfied(ctx, root, n) {
		return false
	}
	rep := verify.PlanNode(ctx, root, n, e.st.Plan())

	// A discover node is not finished because its work-list file exists. The
	// file is an INPUT to the expansion, and a malformed one must fail the
	// node rather than be adopted as done: adopting it here skipped the parse
	// entirely, which is how a broken work set became a green run.
	//
	// The work set is therefore PARSED before the node can be adopted. If the
	// parse fails the node is not adopted and the failure surfaces when the
	// expansion runs.
	if n.Discover != nil {
		if _, err := plan.LoadUnits(root, *n.Discover); err != nil {
			return false
		}
	}

	// Adoption is "the artifacts already exist", which is only proof of work
	// when the node also touched the scope it declared. An isolated node whose
	// branch shows no change in its scope did NOT do the work, even if the
	// files happen to be there, so it must run rather than be adopted.
	if n.Isolation == IsolationWorktree && len(n.FileScope) > 0 {
		wt := worktree{Path: filepath.Join(e.st.Dir(), "worktrees", trace.Name(n.ID))}
		if msg := e.checkScope(n, wt); msg != "" {
			return false
		}
	}
	if err := e.st.Force(n.ID, plan.StatusDone, "adopted: artifacts already satisfy the contract", &rep, time.Now()); err != nil {
		return false
	}
	_ = e.tr.Emit(e.opt.Owner, trace.Event{Run: e.st.Plan().Run, Worker: e.opt.Owner, Node: n.ID, Event: "node.adopt", Message: "artifacts already valid"})
	fmt.Fprintf(e.opt.Out, "adopted %s (%s)\n", n.ID, n.Kind)
	return true
}

func (e *Executor) finishDirect(n *plan.Node, status, reason string) {
	if err := e.st.Force(n.ID, status, reason, nil, time.Now()); err != nil {
		e.setErr(err)
	}
	_ = e.tr.Emit(e.opt.Owner, trace.Event{Run: e.st.Plan().Run, Worker: e.opt.Owner, Node: n.ID, Event: "node." + status, Message: reason})
}

func (e *Executor) summarize() Summary {
	snap, err := e.st.Snapshot()
	if err != nil {
		return Summary{}
	}
	var s Summary
	for _, n := range e.st.All(snap) {
		ns := snap.Nodes[n.ID]
		if ns == nil {
			continue
		}
		switch ns.Status {
		case plan.StatusDone:
			s.Done++
		case plan.StatusFailed:
			s.Failed++
			s.Remaining = append(s.Remaining, n.ID)
		case plan.StatusSkipped:
			s.Skipped++
		default:
			s.Pending++
			s.Remaining = append(s.Remaining, n.ID)
		}
	}
	return s
}

func (e *Executor) setErr(err error) {
	e.errMu.Lock()
	defer e.errMu.Unlock()
	if e.firstErr == nil {
		e.firstErr = err
	}
}

// sessionEnvDrop are variables that would bind a child process to the session
// that launched dagrun instead of making it an independent worker.
//
// This is not cosmetic. Observed: a child jcode inherited the parent's
// JCODE_RUNTIME_PROVIDER/JCODE_ACTIVE_PROVIDER and routed a request to the
// wrong provider, returning HTTP 400 "unknown model" for a model that works
// fine when the child starts clean.
var sessionEnvDrop = map[string]bool{
	"JCODE_SOCKET":                    true,
	"JCODE_SESSION_ID":                true,
	"JCODE_RUNTIME_PROVIDER":          true,
	"JCODE_ACTIVE_PROVIDER":           true,
	"JCODE_INITIAL_PROVIDER_EXPLICIT": true,
	"JCODE_RESUMING":                  true,
	"JCODE_NON_INTERACTIVE":           true,
	"CODEX_SESSION_ID":                true,
	"FORGE_SESSION_ID":                true,
	"HELIOSLITE_SESSION_ID":           true,
}

var sessionEnvDropPrefix = []string{"JCODE_TUI_INHERITED_"}

// childEnv gives each child a clean, non-interactive environment: the parent's
// session and provider bindings are removed so per-node provider/model choices
// actually take effect.
func childEnv() []string {
	src := os.Environ()
	env := make([]string, 0, len(src)+2)
	for _, kv := range src {
		i := strings.IndexByte(kv, '=')
		if i <= 0 {
			continue
		}
		k := kv[:i]
		if sessionEnvDrop[k] {
			continue
		}
		drop := false
		for _, pre := range sessionEnvDropPrefix {
			if strings.HasPrefix(k, pre) {
				drop = true
				break
			}
		}
		if drop {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "DAGRUN=1", "CI=1")
}
