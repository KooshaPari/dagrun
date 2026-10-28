package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/KooshaPari/dagrun/internal/plan"
	"github.com/KooshaPari/dagrun/internal/store"
	"github.com/KooshaPari/dagrun/internal/verify"
)

// cmdExplain diagnoses one node: which predicate failed, why, and what to do.
//
// The command is a composition aid: exit 0 means the node is done, exit 1 means
// it is not, so `dagrun explain 1.2 || dagrun claim 1.2` is a legal shell
// pipeline. All reasoning is delegated to verify (the predicates) and
// verify.Hint (the instruction); nothing here re-implements either.
func cmdExplain(args []string) error {
	code, err := explain(args, os.Stdout)
	if err != nil {
		return err
	}
	if code != 0 {
		os.Exit(code)
	}
	return nil
}

// explainNode is the JSON shape emitted by `dagrun explain NODE --json`.
type explainNode struct {
	Node      string           `json:"node"`
	Kind      string           `json:"kind,omitempty"`
	Title     string           `json:"title,omitempty"`
	Status    string           `json:"status"`
	OK        bool             `json:"ok"`
	Findings  []explainFinding `json:"findings"`
	Check     *explainCheck    `json:"check,omitempty"`
	Verdict   string           `json:"verdict"`
	Hint      string           `json:"hint"`
	BlockedBy []explainDep     `json:"blocked_by"`
}

type explainFinding struct {
	Path   string `json:"path"`
	OK     bool   `json:"ok"`
	Code   string `json:"code,omitempty"`
	Detail string `json:"detail,omitempty"`
	Bytes  int64  `json:"bytes,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
}

type explainCheck struct {
	Command  string `json:"command"`
	OK       bool   `json:"ok"`
	ExitCode int    `json:"exit_code"`
	Detail   string `json:"detail,omitempty"`
}

type explainDep struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// explain does the work and returns the process exit code, so tests can drive
// it without os.Exit. A non-nil error is a usage/IO failure (main exits 1).
func explain(args []string, out io.Writer) (int, error) {
	var dir string
	var jsonOut bool
	flags, pos := splitArgs(args)
	fs := flagSet("explain", &dir, &jsonOut)
	if err := fs.Parse(flags); err != nil {
		return 2, err
	}
	if len(pos) == 0 {
		return 2, fmt.Errorf("usage: dagrun explain NODE [--json] [--dir DIR]")
	}
	want := pos[0]

	p, st, _, err := openRun(dir)
	if err != nil {
		return 1, err
	}
	snap, err := st.Snapshot()
	if err != nil {
		return 1, err
	}

	n := store.Resolve(snap, want)
	if n == nil {
		n = p.ByID(want)
	}
	if n == nil {
		return 1, unknownNodeError(p, st, snap, want)
	}

	ns := snap.Nodes[n.ID]
	if ns == nil {
		ns = &store.NodeState{Status: plan.StatusPending}
	}

	// An isolated node's artifacts live on its branch, not in the shared tree.
	root := st.Root()
	if ns.Worktree != "" {
		root = ns.Worktree
	}

	rep := verify.Node(context.Background(), root, n)
	done := ns.Status == plan.StatusDone
	blocked := blockingDeps(snap, n)

	view := explainNode{
		Node:      n.ID,
		Kind:      n.Kind,
		Title:     n.Title,
		Status:    ns.Status,
		OK:        done && rep.OK,
		Findings:  findingsOf(rep),
		Check:     checkOf(rep),
		Verdict:   verdict(n, ns, rep, done, blocked),
		Hint:      hintFor(rep, done, blocked, n, ns),
		BlockedBy: blocked,
	}
	if view.Findings == nil {
		view.Findings = []explainFinding{}
	}

	if jsonOut {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(view); err != nil {
			return 1, err
		}
	} else {
		writeExplain(out, n, ns, rep, view, time.Now())
	}

	if done {
		return 0, nil
	}
	return 1, nil
}

// hintFor composes the "fix:" line. An already-done node needs no instruction.
func hintFor(rep verify.Report, done bool, blocked []explainDep, n *plan.Node, ns *store.NodeState) string {
	if done {
		if rep.OK {
			return ""
		}
		return "the node is recorded done but its artifacts no longer verify; restore them, or rerun: dagrun plan FILE --reset"
	}
	if len(blocked) > 0 {
		// The node cannot even be claimed, so the blocker is the next action,
		// ahead of any predicate its own contract is missing.
		d := blocked[0]
		return fmt.Sprintf("finish %s first (it is %s); see: dagrun explain %s", d.ID, d.Status, d.ID)
	}
	if h := verify.Hint(n, rep); h != "" {
		return h
	}
	if rep.OK {
		// Artifacts exist and pass: the node simply was never attested.
		return fmt.Sprintf("the artifacts already verify; attest the node: dagrun done %s", n.ID)
	}
	if ns.LastError != "" {
		return fmt.Sprintf("the last attempt failed (%s); inspect the log under the run directory, or retry: dagrun claim %s", oneLineText(ns.LastError), n.ID)
	}
	return fmt.Sprintf("the node is ready with no failing predicate; run it: dagrun claim %s", n.ID)
}

// verdict is the single line naming the first failing predicate.
func verdict(n *plan.Node, ns *store.NodeState, rep verify.Report, done bool, blocked []explainDep) string {
	status := strings.ToUpper(ns.Status)
	if done {
		if rep.OK {
			return fmt.Sprintf("%s DONE  all %d declared artifact(s) verify", n.ID, len(rep.Artifacts))
		}
		return fmt.Sprintf("%s DONE  registered done, but the artifacts no longer verify: %s", n.ID, rep.Reason)
	}
	if len(blocked) > 0 && ns.Status == plan.StatusPending {
		// Not claimable yet, so the dependency is the first thing to say.
		parts := make([]string, 0, len(blocked))
		for _, d := range blocked {
			parts = append(parts, fmt.Sprintf("%s (%s)", d.ID, d.Status))
		}
		return fmt.Sprintf("%s %s  blocked by %s", n.ID, status, strings.Join(parts, ", "))
	}
	for _, res := range rep.Artifacts {
		if res.OK {
			continue
		}
		detail := strings.TrimSpace(res.Code + " " + res.Detail)
		return fmt.Sprintf("%s %s  %s: %s", n.ID, status, res.Path, detail)
	}
	if rep.Check != nil && !rep.Check.OK {
		why := fmt.Sprintf("exited %d", rep.Check.ExitCode)
		if rep.Check.TimedOut {
			why = "timed out"
		}
		return fmt.Sprintf("%s %s  check `%s` %s", n.ID, status, rep.Check.Command, why)
	}
	if rep.OK {
		return fmt.Sprintf("%s %s  artifacts verify but the node is not attested", n.ID, status)
	}
	if ns.LastError != "" {
		return fmt.Sprintf("%s %s  %s", n.ID, status, oneLineText(ns.LastError))
	}
	return fmt.Sprintf("%s %s  no declared predicate is failing", n.ID, status)
}

// blockingDeps lists unsatisfied dependencies with their current status.
func blockingDeps(snap *store.State, n *plan.Node) []explainDep {
	out := []explainDep{}
	for _, d := range n.Deps {
		status := plan.StatusPending
		if ds := snap.Nodes[d]; ds != nil {
			status = ds.Status
		}
		if status != plan.StatusDone && status != plan.StatusSkipped {
			out = append(out, explainDep{ID: d, Status: status})
		}
	}
	return out
}

func findingsOf(rep verify.Report) []explainFinding {
	out := make([]explainFinding, 0, len(rep.Artifacts))
	for _, a := range rep.Artifacts {
		out = append(out, explainFinding{
			Path: a.Path, OK: a.OK, Code: a.Code, Detail: a.Detail,
			Bytes: a.Bytes, SHA256: a.SHA256,
		})
	}
	return out
}

func checkOf(rep verify.Report) *explainCheck {
	if rep.Check == nil {
		return nil
	}
	c := &explainCheck{Command: rep.Check.Command, OK: rep.Check.OK, ExitCode: rep.Check.ExitCode}
	if !rep.Check.OK {
		if rep.Check.TimedOut {
			c.Detail = "timed out"
		} else if first := firstLineOf(rep.Check.Output); first != "" {
			c.Detail = first
		}
	}
	return c
}

// unknownNodeError suggests the nearest ids instead of dying on a typo.
func unknownNodeError(p *plan.Plan, st *store.Store, snap *store.State, want string) error {
	type cand struct {
		id   string
		dist int
	}
	var all []cand
	for _, n := range st.All(snap) {
		if n == nil {
			continue
		}
		d := editDistance(strings.ToLower(want), strings.ToLower(n.ID))
		if strings.HasPrefix(n.ID, want) || strings.HasPrefix(want, n.ID) {
			d = 0
		}
		all = append(all, cand{n.ID, d})
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].dist != all[j].dist {
			return all[i].dist < all[j].dist
		}
		return plan.Less(all[i].id, all[j].id)
	})
	const maxSuggest = 5
	var ids []string
	for i, c := range all {
		if i >= maxSuggest {
			break
		}
		if c.dist <= 2 || len(ids) < 3 {
			ids = append(ids, c.id)
		}
	}
	if len(ids) == 0 {
		return fmt.Errorf("unknown node %q; the run has %d node(s), see: dagrun status", want, len(all))
	}
	return fmt.Errorf("unknown node %q; closest ids: %s (see: dagrun status)", want, strings.Join(ids, ", "))
}

// writeExplain renders the human view: the node, its predicates, the verdict.
func writeExplain(w io.Writer, n *plan.Node, ns *store.NodeState, rep verify.Report, view explainNode, now time.Time) {
	fmt.Fprintf(w, "node %s  kind %s  status %s\n", n.ID, n.Kind, ns.Status)
	if n.Title != "" && n.Title != n.ID {
		fmt.Fprintf(w, "title %s\n", n.Title)
	}
	max := n.MaxAttempts
	if max <= 0 {
		max = 1
	}
	fmt.Fprintf(w, "attempts %d/%d\n", ns.Attempts, max)
	if ns.Status == plan.StatusRunning && ns.Owner != "" {
		lease := ""
		if !ns.LeaseUntil.IsZero() {
			left := ns.LeaseUntil.Sub(now)
			if left < 0 {
				lease = " (lease expired)"
			} else {
				lease = fmt.Sprintf(" (expires in %s)", left.Round(time.Second))
			}
		}
		fmt.Fprintf(w, "owner %s%s\n", ns.Owner, lease)
	}
	if ns.LastError != "" {
		fmt.Fprintf(w, "last error %s\n", oneLineText(ns.LastError))
	}
	if ns.Branch != "" || ns.Worktree != "" || n.Isolation != "" {
		isolation := n.Isolation
		if isolation == "" {
			isolation = "worktree"
		}
		fmt.Fprintf(w, "isolation %s", isolation)
		if ns.Branch != "" {
			fmt.Fprintf(w, "  branch %s", ns.Branch)
		}
		if ns.Worktree != "" {
			fmt.Fprintf(w, "  worktree %s", ns.Worktree)
		}
		fmt.Fprintln(w)
	}

	if len(rep.Artifacts) > 0 {
		fmt.Fprintf(w, "artifacts (%d)\n", len(rep.Artifacts))
		for _, a := range view.Findings {
			state := "ok  "
			if !a.OK {
				state = "FAIL"
			}
			fmt.Fprintf(w, "  %-4s %-40s", state, a.Path)
			switch {
			case !a.OK && a.Code != "":
				fmt.Fprintf(w, " %s %s", a.Code, a.Detail)
			case a.SHA256 != "":
				fmt.Fprintf(w, " %d bytes  sha256:%s", a.Bytes, shortHash(a.SHA256))
			default:
				fmt.Fprintf(w, " %d bytes", a.Bytes)
			}
			fmt.Fprintln(w)
		}
	}
	if view.Check != nil {
		state := "ok"
		if !view.Check.OK {
			state = "FAIL"
		}
		fmt.Fprintf(w, "check %s  %s", state, view.Check.Command)
		if view.Check.Detail != "" {
			fmt.Fprintf(w, "  (%s)", view.Check.Detail)
		}
		fmt.Fprintln(w)
	}
	if len(view.BlockedBy) > 0 {
		fmt.Fprintln(w, "blocked by")
		for _, d := range view.BlockedBy {
			fmt.Fprintf(w, "  %-8s %s\n", d.ID, d.Status)
		}
	}

	fmt.Fprintf(w, "\n%s\n", view.Verdict)
	if view.Hint != "" {
		fmt.Fprintf(w, "  fix: %s\n", view.Hint)
	}
}

func shortHash(s string) string {
	if len(s) > 12 && !strings.Contains(s, " ") {
		return s[:12]
	}
	return s
}

func firstLineOf(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func oneLineText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\n", "; ")
	return strings.TrimSpace(s)
}

// editDistance is plain Levenshtein, used only for "did you mean" hints.
func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := 0; j <= len(rb); j++ {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min3(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}
