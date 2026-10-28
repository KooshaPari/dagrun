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

	"github.com/KooshaPari/dagrun/internal/harness"
	"github.com/KooshaPari/dagrun/internal/plan"
	"github.com/KooshaPari/dagrun/internal/trace"
	"github.com/KooshaPari/dagrun/internal/verify"
)

func (e *Executor) execute(ctx context.Context, n *plan.Node, epoch int64, idx map[string]*plan.Node) {
	start := time.Now()

	// vroot is where this node's artifacts are proven. It is the plan root for a
	// shared checkout and the node's own worktree when it is isolated.
	vroot := e.st.Root()
	workdir := e.st.Root()
	if n.Workdir != "" {
		workdir = filepath.Join(e.st.Root(), n.Workdir)
	}

	if n.Isolation == IsolationWorktree {
		wt, err := ensureWorktree(e.st.Root(), e.st.Dir(), e.st.Plan().Run, n)
		if err != nil {
			e.settleNode(n, epoch, verify.Report{NodeID: n.ID, Reason: err.Error()}, -1, start)
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

	if n.Check != "" && len(n.Artifacts) == 0 {
		rep := verify.Node(ctx, vroot, n)
		e.settleNode(n, epoch, rep, 0, start)
		return
	}
	prompt := plan.Prompt(e.st.Plan(), n, idx)

	name, args, useStdin, err := e.command(n, prompt, workdir)
	if err != nil {
		e.settleNode(n, epoch, verify.Report{NodeID: n.ID, OK: false, Reason: err.Error()}, -1, start)
		return
	}

	logPath := filepath.Join(e.st.Dir(), "logs", trace.Name(n.ID), fmt.Sprintf("%d.log", time.Now().UnixNano()))
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		e.settleNode(n, epoch, verify.Report{NodeID: n.ID, Reason: err.Error()}, -1, start)
		return
	}
	logFile, err := os.Create(logPath)
	if err != nil {
		e.settleNode(n, epoch, verify.Report{NodeID: n.ID, Reason: err.Error()}, -1, start)
		return
	}
	defer logFile.Close()

	cctx, cancel := context.WithTimeout(ctx, n.TimeoutDuration())
	defer cancel()
	cmd := exec.CommandContext(cctx, name, args...)
	cmd.Dir = workdir
	cmd.Stdout = logFile
	cmd.Stderr = logFile
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
	if runErr != nil {
		if ee, ok := runErr.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			exitCode = -1
		}
	}
	_ = e.tr.Emit(e.opt.Owner, trace.Event{Run: e.st.Plan().Run, Worker: e.opt.Owner, Node: n.ID, Event: "node.exit",
		Epoch: epoch, Exit: &exitCode, DurMS: time.Since(start).Milliseconds()})

	select {
	case <-lost:
		// ownership gone: do not touch state
		return
	default:
	}

	rep := verify.Node(ctx, vroot, n)
	e.settleNode(n, epoch, rep, exitCode, start)
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
				Prompt:   prompt,
				Workdir:  workdir,
				Provider: n.Provider,
				Model:    n.Model,
				Tools:    e.opt.Tools,
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

func (e *Executor) settleNode(n *plan.Node, epoch int64, rep verify.Report, exitCode int, start time.Time) {
	status := plan.StatusDone
	reason := ""
	if !rep.OK {
		status = plan.StatusPending // retry, then failed when attempts run out
		reason = rep.Reason
	}
	ok, err := e.st.Finish(n.ID, e.opt.Owner, epoch, status, &rep, exitCode, reason, time.Now())
	if err != nil {
		e.setErr(err)
		return
	}
	if !ok {
		_ = e.tr.Emit(e.opt.Owner, trace.Event{Run: e.st.Plan().Run, Worker: e.opt.Owner, Node: n.ID, Event: "lease.lost", Epoch: epoch})
		return
	}
	if rep.OK {
		fmt.Fprintf(e.opt.Out, "done %s (%s) %dms\n", n.ID, n.Kind, time.Since(start).Milliseconds())
	} else {
		fmt.Fprintf(e.opt.Out, "retry %s: %s\n", n.ID, rep.Reason)
	}
	_ = e.tr.Emit(e.opt.Owner, trace.Event{Run: e.st.Plan().Run, Worker: e.opt.Owner, Node: n.ID, Event: "node." + status,
		Epoch: epoch, Exit: &exitCode, DurMS: time.Since(start).Milliseconds(), Message: rep.Reason})
}
