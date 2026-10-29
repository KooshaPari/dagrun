// Node execution: build the child command, supervise it, then decide the outcome.
//
// The child is never trusted. Whatever it prints is a log; the verdict comes
// from verify.Node over the filesystem after the process exits.
package run

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/KooshaPari/dagrun/internal/diffscope"
	"github.com/KooshaPari/dagrun/internal/harness"
	"github.com/KooshaPari/dagrun/internal/plan"
	"github.com/KooshaPari/dagrun/internal/trace"
	"github.com/KooshaPari/dagrun/internal/verify"
)

// failCtx carries the evidence Classify needs beyond the verify report: how the
// child was started (or why it never was) and whether it ran out of clock.
type failCtx struct {
	spawnErr error
	timedOut bool
}

// verdict is everything settling one node needs, gathered as the node runs.
type verdict struct {
	node     *plan.Node
	epoch    int64
	rep      verify.Report
	exitCode int
	start    time.Time
	workdir  string
	// captured is the tail of what the child printed, kept in memory so the
	// harness's own token accounting can be read without re-reading the log.
	captured []byte
	fail     failCtx
}

func (e *Executor) execute(ctx context.Context, n *plan.Node, epoch int64, idx map[string]*plan.Node) {
	start := time.Now()

	// vroot is where this node's artifacts are proven. It is the plan root for a
	// shared checkout and the node's own worktree when it is isolated.
	vroot := e.st.Root()
	workdir := e.st.Root()
	if n.Workdir != "" {
		workdir = filepath.Join(e.st.Root(), n.Workdir)
	}

	var wt worktree
	if n.Isolation == IsolationWorktree {
		var err error
		wt, err = ensureWorktree(e.st.Root(), e.st.Dir(), e.st.Plan().Run, n)
		if err != nil {
			e.settleNode(ctx, verdict{node: n, epoch: epoch, exitCode: -1, start: start, workdir: workdir,
				rep:  verify.Report{NodeID: n.ID, Reason: err.Error()},
				fail: failCtx{spawnErr: err}})
			return
		}
		workdir, vroot = wt.Path, wt.Path
		if err := e.st.Annotate(n.ID, wt.Path, wt.Branch); err != nil {
			e.setErr(err)
		}
		_ = e.tr.Emit(e.opt.Owner, trace.Event{Run: e.st.Plan().Run, Worker: e.opt.Owner, Node: n.ID,
			Event: "node.worktree", Epoch: epoch, Message: wt.Branch})
		fmt.Fprintf(e.opt.Out, "%s isolated on %s\n", n.ID, wt.Branch)
	}

	// The pre hook runs before any work starts. A failing pre hook means the
	// child is never spawned: the node's environment was not fit to run in.
	if err := e.runHook(ctx, n, epoch, "pre", workdir); err != nil {
		e.settleNode(ctx, verdict{node: n, epoch: epoch, exitCode: -1, start: start, workdir: workdir,
			rep:  verify.Report{NodeID: n.ID, OK: false, Reason: err.Error()},
			fail: failCtx{spawnErr: err}})
		return
	}

	// A check-only node has no child: its evidence already exists on disk.
	if n.Check != "" && len(n.Artifacts) == 0 {
		e.settleNode(ctx, verdict{node: n, epoch: epoch, start: start, workdir: workdir,
			rep: verify.PlanNode(ctx, vroot, n, e.st.Plan())})
		return
	}

	prompt := plan.Prompt(e.st.Plan(), n, idx)

	name, args, useStdin, err := e.command(n, prompt, workdir)
	if err != nil {
		e.settleNode(ctx, verdict{node: n, epoch: epoch, exitCode: -1, start: start, workdir: workdir,
			rep:  verify.Report{NodeID: n.ID, OK: false, Reason: err.Error()},
			fail: failCtx{spawnErr: err}})
		return
	}

	logPath := filepath.Join(e.st.Dir(), "logs", trace.Name(n.ID), fmt.Sprintf("%d.log", time.Now().UnixNano()))
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		e.settleNode(ctx, verdict{node: n, epoch: epoch, exitCode: -1, start: start, workdir: workdir,
			rep:  verify.Report{NodeID: n.ID, Reason: err.Error()},
			fail: failCtx{spawnErr: err}})
		return
	}
	logFile, err := os.Create(logPath)
	if err != nil {
		e.settleNode(ctx, verdict{node: n, epoch: epoch, exitCode: -1, start: start, workdir: workdir,
			rep:  verify.Report{NodeID: n.ID, Reason: err.Error()},
			fail: failCtx{spawnErr: err}})
		return
	}
	defer logFile.Close()

	// The log file is the durable record; the sink also keeps the tail in
	// memory, which is where the harness prints its usage object.
	sink := newLogSink(logFile)

	cctx, cancel := context.WithTimeout(ctx, n.TimeoutDuration())
	defer cancel()
	cmd := exec.CommandContext(cctx, name, args...)
	cmd.Dir = workdir
	cmd.Stdout = sink
	cmd.Stderr = sink
	cmd.Env = childEnv()
	if useStdin {
		cmd.Stdin = strings.NewReader(prompt)
	}
	setProcessGroup(cmd)

	if e.opt.Verbose {
		fmt.Fprintf(e.opt.Out, "spawn %s -> %s %s\n", n.ID, name, strings.Join(args, " "))
	}
	_ = e.tr.Emit(e.opt.Owner, trace.Event{Run: e.st.Plan().Run, Worker: e.opt.Owner, Node: n.ID, Event: "node.spawn", Epoch: epoch, Message: name})

	// heartbeat until the child exits; losing the lease kills the child so a
	// zombie cannot write a result over its successor's.
	hbCtx, hbCancel := context.WithCancel(cctx)
	defer hbCancel()
	lost := make(chan struct{}, 1)
	go e.heartbeatLoop(hbCtx, n.ID, epoch, cancel, lost)

	runErr := cmd.Run()
	hbCancel()
	exitCode := 0
	var spawnErr error
	if runErr != nil {
		if ee, ok := runErr.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			// Not a non-zero exit: the binary never ran. That is a different
			// failure class from "the agent ran and produced the wrong thing".
			exitCode = -1
			spawnErr = runErr
		}
	}
	_ = e.tr.Emit(e.opt.Owner, trace.Event{Run: e.st.Plan().Run, Worker: e.opt.Owner, Node: n.ID, Event: "node.exit",
		Epoch: epoch, Exit: &exitCode, DurMS: time.Since(start).Milliseconds()})

	select {
	case <-lost:
		// ownership gone: do not touch state. A killed child is a failed
		// attempt for whoever adopts the node next, not a result.
		return
	default:
	}

	rep := verify.PlanNode(ctx, vroot, n, e.st.Plan())

	// Coherence: an isolated node that declared a file_scope must have
	// actually touched it. Its worktree branch is the evidence.
	if rep.OK && n.Isolation == IsolationWorktree && len(n.FileScope) > 0 {
		if msg := e.checkScope(n, wt); msg != "" {
			rep.OK = false
			rep.Reason = msg
			rep.Artifacts = append(rep.Artifacts, verify.ArtifactResult{
				Path:   strings.Join(n.FileScope, ", "),
				OK:     false,
				Code:   "scope_untouched",
				Detail: msg,
			})
		}
	}
	e.settleNode(ctx, verdict{
		node:     n,
		epoch:    epoch,
		rep:      rep,
		exitCode: exitCode,
		start:    start,
		workdir:  workdir,
		captured: sink.tail.Bytes(),
		fail:     failCtx{spawnErr: spawnErr, timedOut: cctx.Err() == context.DeadlineExceeded},
	})
}

// checkScope verifies that an isolated node changed at least one file inside
// the scope it declared.
//
// The check is SKIPPED when the evidence is unavailable: a plan root that is
// not a git repository, or a branch with no base to compare against. A skipped
// check is not a passed check, and the report says so.
func (e *Executor) checkScope(n *plan.Node, wt worktree) string {
	repo := e.st.Root()
	changes, err := diffscope.WorktreeChanges(repo, "HEAD", wt.Branch, wt.Path)
	if err != nil {
		return fmt.Sprintf("scope check skipped (no git evidence): %v", err)
	}
	if len(changes) == 0 {
		return fmt.Sprintf("scope_untouched: the node changed nothing, so it did not do the work it declared (scope: %s)",
			strings.Join(n.FileScope, ", "))
	}
	if !diffscope.Touches(changes, n.FileScope) {
		var paths []string
		for _, c := range changes {
			paths = append(paths, c.Path)
		}
		return fmt.Sprintf("scope_untouched: the node changed %s, which is outside its declared scope (%s)",
			strings.Join(paths, ", "), strings.Join(n.FileScope, ", "))
	}
	return ""
}

// command picks the executable. Precedence: an explicit --exec-template wins
// over everything (that is what "explicit" means), then harness: exec, then
// the named or auto-detected harness, then the template as a fallback.
func (e *Executor) command(n *plan.Node, prompt, workdir string) (string, []string, bool, error) {
	explicit := e.opt.ExecTemplate != "" && (n.Harness == "exec" || n.Harness == "auto")
	if !explicit && n.Harness != "exec" {
		a, err := harness.Resolve(n.Harness)
		if err == nil && a != nil {
			path, args, cerr := a.Command(harness.Spec{
				Prompt:      prompt,
				Workdir:     workdir,
				Provider:    n.Provider,
				Model:       n.Model,
				Tools:       e.opt.Tools,
				ToolProfile: n.ToolProfile,
			})
			return path, args, false, cerr
		}
		if err != nil && e.opt.ExecTemplate == "" {
			return "", nil, false, err
		}
	}
	if e.opt.ExecTemplate == "" {
		return "", nil, false, fmt.Errorf("node %s needs harness exec but no --exec-template is configured", n.ID)
	}
	return harness.ExecCommand(e.opt.ExecTemplate, prompt, workdir, n.Model)
}

func (e *Executor) heartbeatLoop(ctx context.Context, nodeID string, epoch int64, cancel context.CancelFunc, lost chan<- struct{}) {
	interval := e.opt.Lease / 3
	if interval < 2*time.Second {
		interval = 2 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			ok, err := e.st.Heartbeat(nodeID, e.opt.Owner, epoch, e.opt.Lease, time.Now())
			if err != nil || !ok {
				select {
				case lost <- struct{}{}:
				default:
				}
				cancel()
				return
			}
		}
	}
}

// settle records one node's outcome: usage and duration first, then the
// contract hooks, then the status with the retry policy applied.
//
// The order is deliberate. Usage is measured rather than asserted. A post hook
// can still turn a verified node into a failure, so the failure class is
// computed after the hooks have run. And the class decides whether "pending"
// (retry) or "failed" is written, so the status comes last.
func (e *Executor) settleNode(ctx context.Context, v verdict) {
	n, epoch := v.node, v.epoch
	e.recordUsage(n, epoch, v.captured, time.Since(v.start))

	rep := v.rep
	fail := v.fail
	if rep.OK {
		if err := e.runHook(ctx, n, epoch, "post", v.workdir); err != nil {
			// The hook is part of the contract: artifacts that verify but a
			// post hook that fails is not a delivered node. It classifies as
			// a contract miss rather than a harness error, because the child
			// itself did run and did produce its artifacts.
			rep.OK = false
			rep.Reason = err.Error()
			fail = failCtx{}
		}
	}

	class := FailureNone
	if !rep.OK {
		class = Classify(&rep, checkErr(rep), fail.spawnErr, fail.timedOut, v.exitCode)
		if _, err := e.st.SetFailureClass(n.ID, e.opt.Owner, epoch, string(class)); err != nil {
			e.setErr(err)
		}
		// Feed the outcome back to per-provider admission control. A provider
		// that refused or timed out is put into a cooldown so the next nodes
		// routed to it wait instead of hammering it. A contract miss is the
		// AGENT's fault, not the provider's, so it must NOT cool the provider
		// down: doing so would punish every other node that shares the
		// provider for something one agent got wrong.
		if e.opt.Admission != nil {
			provider := plan.ProviderOf(n)
			limits := e.opt.ProviderPolicy.LimitsFor(provider)
			if isProviderFailureClass(class) {
				e.opt.Admission.Fail(provider, limits, time.Now())
				_ = e.tr.Emit(e.opt.Owner, trace.Event{
					Run: e.st.Plan().Run, Worker: e.opt.Owner, Node: n.ID,
					Event: "provider.cooldown",
					Attrs: map[string]any{"provider": provider, "class": string(class)},
				})
			} else {
				e.opt.Admission.Success(provider)
			}
		}
		if err := e.runHook(ctx, n, epoch, "on_fail", v.workdir); err != nil {
			// The node already failed. A broken cleanup is reported, never
			// promoted over the failure that came first.
			fmt.Fprintf(e.opt.Out, "on_fail hook for %s also failed: %v\n", n.ID, err)
		}
	} else if e.opt.Admission != nil {
		e.opt.Admission.Success(plan.ProviderOf(n))
	}

	status := plan.StatusDone
	reason := ""
	if !rep.OK {
		reason = rep.Reason
		// When the harness itself failed, the artifact reason is a
		// CONSEQUENCE, not the cause: "bad.txt: missing file" hides the fact
		// that the provider refused the work. Say so, or the operator reads a
		// contract problem where there was a provider outage.
		if v.exitCode != 0 && class == FailureHarnessError {
			reason = fmt.Sprintf("harness exited %d: %s", v.exitCode, rep.Reason)
		}
		status = e.retryOrFail(n, class)
	}

	ok, err := e.st.Finish(n.ID, e.opt.Owner, epoch, status, &rep, v.exitCode, reason, time.Now())
	if err != nil {
		e.setErr(err)
		return
	}
	if !ok {
		_ = e.tr.Emit(e.opt.Owner, trace.Event{Run: e.st.Plan().Run, Worker: e.opt.Owner, Node: n.ID, Event: "lease.lost", Epoch: epoch})
		return
	}
	switch {
	case rep.OK:
		fmt.Fprintf(e.opt.Out, "done %s (%s) %dms\n", n.ID, n.Kind, time.Since(v.start).Milliseconds())
	case status == plan.StatusFailed:
		fmt.Fprintf(e.opt.Out, "fail %s (%s): %s\n", n.ID, FailureSummary(class), rep.Reason)
	default:
		fmt.Fprintf(e.opt.Out, "retry %s (%s): %s\n", n.ID, FailureSummary(class), rep.Reason)
	}
	_ = e.tr.Emit(e.opt.Owner, trace.Event{Run: e.st.Plan().Run, Worker: e.opt.Owner, Node: n.ID, Event: "node." + status,
		Epoch: epoch, Exit: &v.exitCode, DurMS: time.Since(v.start).Milliseconds(), Message: rep.Reason})
}

// retryOrFail applies the retry policy. A failed node goes back to pending only
// when the cause is worth another attempt and the attempt budget allows it;
// otherwise it is failed for good, so the scheduler stops picking it up.
func (e *Executor) retryOrFail(n *plan.Node, class FailureClass) string {
	attempts, known := e.st.Attempts(n.ID)
	if !known {
		// Unreadable state is not evidence against a retry: Claim still
		// enforces the attempt budget, so a pending node cannot loop forever.
		return plan.StatusPending
	}
	max := n.MaxAttempts
	if max < 1 {
		max = 1
	}
	if !ShouldRetry(class, attempts, max) {
		return plan.StatusFailed
	}
	return plan.StatusPending
}

// checkErr turns a failing check result into the error Classify expects, so a
// failed check is not mistaken for a contract miss.
func checkErr(rep verify.Report) error {
	if rep.Check == nil || rep.Check.OK {
		return nil
	}
	if rep.Check.Output != "" {
		return fmt.Errorf("check %s exited %d: %s", rep.Check.Command, rep.Check.ExitCode, rep.Check.Output)
	}
	return fmt.Errorf("check %s exited %d", rep.Check.Command, rep.Check.ExitCode)
}
