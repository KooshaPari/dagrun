package run

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/KooshaPari/dagrun/internal/plan"
	"github.com/KooshaPari/dagrun/internal/trace"
)

// Isolation names for plan.Node.Isolation.
const (
	IsolationShared   = ""
	IsolationWorktree = "worktree"
)

// worktree is the isolated checkout a node runs in.
type worktree struct {
	Path   string
	Branch string
}

// ensureWorktree creates (or reuses) a git worktree for one node.
//
// This is what makes parallel IMPL nodes safe: two nodes editing the same
// checkout collide, and fan-out makes several IMPL nodes in flight the normal
// case rather than an edge case. Each node gets its own branch and directory,
// and the branch is left in place so a human or a later node can merge it.
func ensureWorktree(root, runDir, runName string, n *plan.Node) (worktree, error) {
	name := trace.Name(n.ID)
	wt := worktree{
		Path:   filepath.Join(runDir, "worktrees", name),
		Branch: "dagrun/" + branchPart(runName) + "/" + branchPart(n.ID),
	}

	if isWorktree(wt.Path) {
		return wt, nil
	}
	if err := os.MkdirAll(filepath.Dir(wt.Path), 0o755); err != nil {
		return wt, err
	}

	if gitSucceeds(root, "rev-parse", "--verify", "--quiet", "refs/heads/"+wt.Branch) {
		// Branch already exists (a previous attempt): attach to it rather than
		// resetting it, so earlier work in the branch is not discarded.
		if out, err := git(root, "worktree", "add", wt.Path, wt.Branch); err != nil {
			return wt, fmt.Errorf("git worktree add %s: %v: %s", wt.Branch, err, out)
		}
		return wt, nil
	}

	if out, err := git(root, "worktree", "add", "-b", wt.Branch, wt.Path, "HEAD"); err != nil {
		return wt, fmt.Errorf("git worktree add -b %s: %v: %s", wt.Branch, err, out)
	}
	return wt, nil
}

// isWorktree reports whether path is already a linked worktree.
func isWorktree(path string) bool {
	if _, err := os.Stat(path); err != nil {
		return false
	}
	// A linked worktree has a .git file (not directory) pointing at the parent.
	info, err := os.Lstat(filepath.Join(path, ".git"))
	return err == nil && info.Mode().IsRegular()
}

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func gitSucceeds(dir string, args ...string) bool {
	_, err := git(dir, args...)
	return err == nil
}

// branchPart makes an id safe for a git branch component.
func branchPart(s string) string {
	out := plan.Slug(s)
	out = strings.ReplaceAll(out, ".", "-")
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	out = strings.Trim(out, "-")
	if out == "" {
		out = "node"
	}
	if len(out) > 60 {
		out = out[:60]
	}
	return out
}
