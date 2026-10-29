package hook

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// defaultTimeout is applied when the caller's context has no deadline.
const defaultTimeout = 60 * time.Second

// maxOutputLen bounds the captured output embedded in an error.
const maxOutputLen = 4000

// Hooks are the shell commands a plan node can run around its execution.
type Hooks struct {
	Pre    string `yaml:"pre,omitempty" json:"pre,omitempty"`
	Post   string `yaml:"post,omitempty" json:"post,omitempty"`
	OnFail string `yaml:"on_fail,omitempty" json:"on_fail,omitempty"`
}

// Run executes the hook for a phase ("pre", "post" or "on_fail").
//
// An empty hook for that phase is a no-op. The command runs with `sh -c` in
// dir, with env appended, a timeout (default 60s), and its output captured.
// A failing hook returns an error containing that output.
func Run(ctx context.Context, phase string, h Hooks, dir string, env []string) error {
	var cmd string
	switch phase {
	case "pre":
		cmd = h.Pre
	case "post":
		cmd = h.Post
	case "on_fail":
		cmd = h.OnFail
	default:
		return fmt.Errorf("hook: unknown phase %q", phase)
	}

	if strings.TrimSpace(cmd) == "" {
		return nil
	}

	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultTimeout)
		defer cancel()
	}

	c := exec.CommandContext(ctx, "sh", "-c", cmd)
	c.Dir = dir
	c.Env = append(os.Environ(), env...)

	out, err := c.CombinedOutput()
	if err != nil {
		return fmt.Errorf("hook %s failed: %w: %s", phase, err, truncate(string(out), maxOutputLen))
	}
	return nil
}

// DefaultGitCommitHook returns a shell snippet that commits the worktree
// using the message in DAGRUN_COMMIT_MSG.
func DefaultGitCommitHook(root string) string {
	_ = root
	return "git add -A && git commit -m \"${DAGRUN_COMMIT_MSG:-dagrun: verified node}\" || true"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(truncated)"
}
