// Package diffscope answers one question: did a node actually touch the files
// it declared?
//
// A node declares file_scope as the set of paths it may edit. With per-node git
// worktrees the answer is measurable rather than assumed: compare the branch
// against its base and see which paths changed.
package diffscope

import (
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Change is one path that differs between a branch and its base.
type Change struct {
	Path   string `json:"path"`
	Status string `json:"status"` // A added, M modified, D deleted, R renamed
}

// Diff returns the paths that differ between ref and its merge base with base.
//
// It shells out to git on purpose: the answer must be the repository's own
// truth, not a re-implementation of it.
func Diff(repo, base, ref string) ([]Change, error) {
	out, err := git(repo, "diff", "--name-status", "--no-renames", base+"..."+ref)
	if err != nil {
		return nil, err
	}
	return parseNameStatus(out), nil
}

// WorktreeChanges returns every path a worktree changed, whether or not it was
// committed.
//
// An agent usually leaves its work uncommitted: the branch is where a human
// merges it from, not a place the agent commits to. Looking only at committed
// changes would therefore report "the node changed nothing" for a node that
// did exactly what it was asked.
func WorktreeChanges(repo, base, branch, worktree string) ([]Change, error) {
	committed, err := Diff(repo, base, branch)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, c := range committed {
		seen[c.Path] = true
	}

	// The worktree's own status covers uncommitted edits and untracked files.
	out, err := git(worktree, "status", "--porcelain")
	if err == nil {
		for _, line := range strings.Split(out, "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			// porcelain format: two status columns, a space, then the path.
			// Trimming the whole line first would eat a leading path character,
			// turning "src/a.go" into "rc/a.go".
			if len(line) < 4 {
				continue
			}
			status := strings.TrimSpace(line[:2])
			path := strings.TrimSpace(line[2:])
			path = strings.TrimPrefix(path, " ")
			if i := strings.Index(path, " -> "); i >= 0 {
				path = path[i+4:]
			}
			if seen[path] {
				continue
			}
			seen[path] = true
			committed = append(committed, Change{Path: path, Status: status})
		}
	}
	sort.Slice(committed, func(i, j int) bool { return committed[i].Path < committed[j].Path })
	return committed, nil
}

// parseNameStatus turns git diff --name-status output into changes.
func parseNameStatus(out string) []Change {
	var changes []Change
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		changes = append(changes, Change{
			Status: fields[0],
			Path:   fields[len(fields)-1],
		})
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes
}

// Touches reports whether any change falls inside scope.
//
// A scope entry is a glob, matched with the same rules as Matches: "*" stays
// inside one path segment and "**" spans directories.
func Touches(changes []Change, scope []string) bool {
	for _, c := range changes {
		for _, s := range scope {
			if Matches(s, c.Path) {
				return true
			}
		}
	}
	return false
}

// Matches reports whether a path is covered by a scope glob.
//
// The rule is deliberately simple and documented, because a scope that quietly
// does not match is worse than no scope at all:
//
//   - a pattern with no glob characters matches that exact path only
//   - "**" spans any number of directories, including none
//   - "*" matches within one path segment and never crosses a "/"
//   - "?" matches one character within a segment
//
// So "src/*.go" covers "src/a.go" but NOT "src/a/b.go"; to cover a subtree
// write "src/**/*.go". That is the same rule git uses for gitignore pathspecs,
// and it means "declare what you mean" instead of "declare something nearby".
func Matches(pattern, path string) bool {
	if pattern == "" {
		return false
	}
	pattern = filepath.ToSlash(pattern)
	path = filepath.ToSlash(path)
	return matchSegments(strings.Split(pattern, "/"), strings.Split(path, "/"))
}

// matchSegments matches a pattern's segments against a path's segments.
func matchSegments(pat, seg []string) bool {
	if len(pat) == 0 {
		return len(seg) == 0
	}
	if pat[0] == "**" {
		// "**" may consume zero or more segments
		for i := 0; i <= len(seg); i++ {
			if matchSegments(pat[1:], seg[i:]) {
				return true
			}
		}
		return false
	}
	if len(seg) == 0 {
		return false
	}
	if !matchSegment(pat[0], seg[0]) {
		return false
	}
	return matchSegments(pat[1:], seg[1:])
}

// matchSegment matches one path segment against one pattern segment.
func matchSegment(pat, seg string) bool {
	if pat == "*" {
		return seg != ""
	}
	if pat == "" {
		return seg == ""
	}
	if len(pat) == 1 && pat == "?" {
		return len(seg) == 1
	}
	// fall back to the standard matcher for the general case
	ok, err := filepath.Match(pat, seg)
	return err == nil && ok
}

// git runs a git command in repo and returns stdout.
func git(repo string, args ...string) (string, error) {
	full := append([]string{"-C", repo}, args...)
	cmd := exec.Command("git", full...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", &Error{Args: args, Output: strings.TrimSpace(string(out)), Err: err}
	}
	return strings.TrimSpace(string(out)), nil
}

// Error is a git failure with its output.
type Error struct {
	Args   []string
	Output string
	Err    error
}

func (e *Error) Error() string {
	return "git " + strings.Join(e.Args, " ") + ": " + e.Err.Error() + ": " + e.Output
}
