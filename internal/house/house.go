// Package house implements run-directory housekeeping: a read-only health
// check and a conservative garbage collector.
//
// The collector's governing rule is that it must never lose work. Anything it
// is not certain about is kept and reported, because a stale worktree is a
// nuisance while a deleted unmerged branch is a loss.
package house

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/KooshaPari/dagrun/internal/harness"
	"github.com/KooshaPari/dagrun/internal/plan"
	"github.com/KooshaPari/dagrun/internal/store"
)

// Severity levels for a doctor finding.
const (
	SeverityInfo  = "info"
	SeverityWarn  = "warn"
	SeverityError = "error"
)

// Finding is one observation about a run directory.
type Finding struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Detail   string `json:"detail"`
	Action   string `json:"action,omitempty"`
}

// Actionable reports whether a finding should make `doctor` exit non-zero.
func (f Finding) Actionable() bool {
	return f.Severity == SeverityError
}

// Report is the doctor result.
type Report struct {
	Dir      string         `json:"dir"`
	Run      string         `json:"run,omitempty"`
	Findings []Finding      `json:"findings"`
	Counts   map[string]int `json:"counts,omitempty"`
	DiskFree uint64         `json:"disk_free_bytes,omitempty"`
}

// Healthy reports whether nothing actionable was found.
func (r *Report) Healthy() bool {
	for _, f := range r.Findings {
		if f.Actionable() {
			return false
		}
	}
	return true
}

// Doctor inspects a run directory without changing anything.
func Doctor(dir string, st *store.Store, snap *store.State) *Report {
	rep := &Report{Dir: dir, Findings: []Finding{}, Counts: map[string]int{}}
	add := func(code, sev, detail, action string) {
		rep.Findings = append(rep.Findings, Finding{Code: code, Severity: sev, Detail: detail, Action: action})
	}

	if _, err := os.Stat(dir); err != nil {
		add("missing_dir", SeverityError, fmt.Sprintf("run directory %s is missing", dir),
			"run `dagrun plan FILE` to create it")
		return rep
	}
	if _, err := os.Stat(filepath.Join(dir, "run.yaml")); err != nil {
		add("missing_plan", SeverityError, "run.yaml is missing", "reinstall the plan with `dagrun plan FILE`")
	}
	if _, err := os.Stat(filepath.Join(dir, "state.json")); err != nil {
		add("missing_state", SeverityError, "state.json is missing", "the run has no progress recorded")
	}
	if st != nil && snap != nil {
		rep.Run = st.Plan().Run
		for _, n := range st.Plan().Nodes {
			if n == nil {
				continue
			}
			if ns := snap.Nodes[n.ID]; ns != nil {
				rep.Counts[ns.Status]++
			}
		}
	}

	if snap == nil {
		return rep
	}
	now := time.Now()
	host, _ := os.Hostname()
	for id, ns := range snap.Nodes {
		if ns == nil {
			continue
		}
		if ns.Status == plan.StatusRunning && !ns.LeaseUntil.After(now) {
			add("expired_lease", SeverityError,
				fmt.Sprintf("node %s is running but its lease expired at %s", id, ns.LeaseUntil.Format(time.RFC3339)),
				fmt.Sprintf("run `dagrun status` then `dagrun run` to reclaim it"))
		}
		if ns.Status == plan.StatusRunning && ns.Owner != "" && host != "" && !strings.Contains(ns.Owner, host) {
			add("foreign_owner", SeverityInfo,
				fmt.Sprintf("node %s is leased by %s on another host", id, ns.Owner), "")
		}
	}

	// dynamic children whose parent is gone
	for id := range snap.Dynamic {
		parent := id
		if i := strings.LastIndex(id, "/"); i > 0 {
			parent = id[:i]
		}
		if parent == id {
			continue
		}
		if _, ok := snap.Nodes[parent]; !ok {
			add("orphan_child", SeverityError,
				fmt.Sprintf("dynamic node %s has no parent %s in this run", id, parent), "")
		}
	}

	// stale worktrees: on disk, but no node references them
	wtDir := filepath.Join(dir, "worktrees")
	if entries, err := os.ReadDir(wtDir); err == nil {
		referenced := map[string]bool{}
		for _, n := range snap.Nodes {
			if n != nil && n.Worktree != "" {
				referenced[filepath.Clean(n.Worktree)] = true
			}
		}
		for _, e := range entries {
			full := filepath.Join(wtDir, e.Name())
			if referenced[filepath.Clean(full)] {
				continue
			}
			branch := branchOf(full)
			add("stale_worktree", SeverityWarn,
				fmt.Sprintf("worktree %s (%s) belongs to no node", e.Name(), branch),
				"run `dagrun gc --older-than 24h` to remove it once merged")
		}
	}

	if free, err := diskFree(dir); err == nil {
		rep.DiskFree = free
		if free < 64<<20 {
			add("low_disk", SeverityWarn,
				fmt.Sprintf("only %d MiB free on the run filesystem", free>>20), "free space or move the run directory")
		}
	}

	// st can be nil when the caller only wants a directory health check, so the
	// preflight is skipped rather than dereferenced.
	if st != nil {
		for _, f := range HarnessFindings(st.Plan().Nodes) {
			rep.Findings = append(rep.Findings, f)
		}
	}
	return rep
}

// checkHarnesses reports nodes whose harness cannot actually run.
//
// This is a PREFLIGHT check, and it exists because of a measured cost: a run
// assigned to an uninstalled harness otherwise discovers the problem one spawn
// at a time, burning the whole attempt budget per node before anyone notices.
// A run that cannot possibly succeed should say so before it starts, not after
// an hour.
//
// Only an EXPLICIT harness is checked. "auto" means "pick an installed one", so
// it is viable by definition; "exec" is a command template with no binary to
// look for; and a check-only node has no child at all.
// HarnessFindings reports nodes whose harness cannot run.
//
// It is exported so `dagrun run` can refuse a plan it cannot execute BEFORE it
// burns an attempt budget per node. An unattended run has nobody watching to
// notice the pattern, so the refusal is the only useful moment to speak.
func HarnessFindings(nodes []*plan.Node) []Finding {
	var out []Finding
	installed := map[string]bool{}
	for _, info := range harness.Detect() {
		installed[info.Name] = true
	}
	checkHarnesses(nodes, installed, func(code, sev, detail, action string) {
		out = append(out, Finding{Code: code, Severity: sev, Detail: detail, Action: action})
	})
	return out
}

// installed is injected rather than probed so the rule is testable on a host
// where every known harness happens to be present. A check that can only be
// exercised by uninstalling something is not a check.
func checkHarnesses(nodes []*plan.Node, installed map[string]bool, add func(code, sev, detail, action string)) {
	seen := map[string]bool{}
	for _, n := range nodes {
		if n == nil || n.Harness == "" || n.Harness == "auto" || n.Harness == "exec" {
			continue
		}
		// A node with no artifacts and a check has nothing to spawn.
		if n.Check != "" && len(n.Artifacts) == 0 {
			continue
		}
		if installed[n.Harness] || seen[n.Harness] {
			continue
		}
		seen[n.Harness] = true
		if _, err := harness.Resolve(n.Harness); err != nil {
			add("harness_unknown", SeverityError,
				fmt.Sprintf("node %s names harness %q, which dagrun does not know", n.ID, n.Harness),
				"fix the plan's harness, or run `dagrun detect` to see the supported set")
			continue
		}
		add("harness_missing", SeverityError,
			fmt.Sprintf("node %s names harness %q, which is not installed", n.ID, n.Harness),
			fmt.Sprintf("install %s, or set the node's harness to exec with a command template", n.Harness))
	}
}

// branchOf reads the branch a worktree is on.
func branchOf(worktree string) string {
	cmd := exec.Command("git", "-C", worktree, "rev-parse", "--abbrev-ref", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return "(unknown)"
	}
	return strings.TrimSpace(string(out))
}

// diskFree returns free bytes on the filesystem holding path.
func diskFree(path string) (uint64, error) {
	var st syscallStatfs
	if err := statfs(path, &st); err != nil {
		return 0, err
	}
	return st.Bavail * uint64(st.Bsize), nil
}

// GCResult reports what a collection did or would do.
type GCResult struct {
	Dir        string   `json:"dir"`
	DryRun     bool     `json:"dry_run"`
	FilesFreed []string `json:"files_freed,omitempty"`
	BytesFreed int64    `json:"bytes_freed"`
	Worktrees  []string `json:"worktrees_removed,omitempty"`
	Kept       []string `json:"kept,omitempty"`
	Reasons    []string `json:"reasons,omitempty"`
}

// GC prunes reclaimable run data.
//
// The rules are deliberately conservative. A worktree is removed only when its
// node is terminal, its branch is merged, and it has no uncommitted changes.
// Everything else is kept with a reason.
func GC(dir string, snap *store.State, olderThan time.Duration, dryRun bool) *GCResult {
	res := &GCResult{Dir: dir, DryRun: dryRun}
	cutoff := time.Now().Add(-olderThan)

	// logs and trace shards
	for _, sub := range []string{"logs", "trace"} {
		base := filepath.Join(dir, sub)
		_ = filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
			if err != nil || info == nil || info.IsDir() {
				return nil
			}
			if info.ModTime().After(cutoff) {
				return nil
			}
			res.FilesFreed = append(res.FilesFreed, path)
			res.BytesFreed += info.Size()
			if !dryRun {
				_ = os.Remove(path)
			}
			return nil
		})
	}

	// worktrees
	wtDir := filepath.Join(dir, "worktrees")
	entries, err := os.ReadDir(wtDir)
	if err != nil {
		return res
	}
	byPath := map[string]string{}
	for id, ns := range snap.Nodes {
		if ns != nil && ns.Worktree != "" {
			byPath[filepath.Clean(ns.Worktree)] = id
		}
	}
	for _, e := range entries {
		full := filepath.Join(wtDir, e.Name())
		nodeID, known := byPath[filepath.Clean(full)]
		if !known {
			res.Kept = append(res.Kept, full)
			res.Reasons = append(res.Reasons, full+": no node references it (run with --older-than to age it out)")
			continue
		}
		ns := snap.Nodes[nodeID]
		if ns == nil || (ns.Status != plan.StatusDone && ns.Status != plan.StatusFailed &&
			ns.Status != plan.StatusSkipped && ns.Status != plan.StatusCancelled) {
			res.Kept = append(res.Kept, full)
			res.Reasons = append(res.Reasons, full+": node "+nodeID+" is "+statusOf(ns)+" (not terminal)")
			continue
		}
		if out, err := gitOut(full, "status", "--porcelain"); err == nil && strings.TrimSpace(out) != "" {
			res.Kept = append(res.Kept, full)
			res.Reasons = append(res.Reasons, full+": uncommitted changes")
			continue
		}
		if !branchMerged(full) {
			res.Kept = append(res.Kept, full)
			res.Reasons = append(res.Reasons, full+": branch not merged")
			continue
		}
		res.Worktrees = append(res.Worktrees, full)
		if !dryRun {
			_ = removeWorktree(full)
		}
	}
	sort.Strings(res.Kept)
	sort.Strings(res.Reasons)
	sort.Strings(res.FilesFreed)
	sort.Strings(res.Worktrees)
	return res
}

func statusOf(ns *store.NodeState) string {
	if ns == nil {
		return "unknown"
	}
	return ns.Status
}

// branchMerged reports whether the worktree's branch is merged into HEAD of
// its parent repository.
func branchMerged(worktree string) bool {
	gitDir := gitDirOf(worktree)
	if gitDir == "" {
		return false
	}
	out, err := gitOut(gitDir, "branch", "--merged")
	if err != nil {
		return false
	}
	branch := branchOf(worktree)
	if branch == "" {
		return false
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) == 0 {
			continue
		}
		name := strings.TrimPrefix(fields[0], "*")
		if name == branch {
			return true
		}
	}
	return false
}

// gitDirOf returns the parent repository directory of a linked worktree.
func gitDirOf(worktree string) string {
	raw, err := os.ReadFile(filepath.Join(worktree, ".git"))
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(line, "gitdir:") {
		return ""
	}
	target := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
	// target is <repo>/.git/worktrees/<name>; the repo root is three levels up
	if filepath.Base(target) == "worktrees" {
		return filepath.Dir(filepath.Dir(target))
	}
	return filepath.Dir(target)
}

func gitOut(dir string, args ...string) (string, error) {
	full := append([]string{"-C", dir}, args...)
	cmd := exec.Command("git", full...)
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// removeWorktree detaches and deletes the worktree directory.
func removeWorktree(path string) error {
	gitDir := gitDirOf(path)
	if gitDir != "" {
		_, _ = gitOut(gitDir, "worktree", "remove", "--force", path)
	}
	return os.RemoveAll(path)
}
