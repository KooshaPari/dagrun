// Package verify decides whether a node actually did its job.
//
// The rule is deliberately narrow: a node is done only when every declared
// artifact satisfies its predicate and the optional check command exits 0.
// Nothing here asks a model, reads intent, or trusts a claim.
package verify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/KooshaPari/dagrun/internal/plan"
)

// ArtifactResult is the outcome for one declared artifact.
type ArtifactResult struct {
	Path    string   `json:"path"`
	OK      bool     `json:"ok"`
	Code    string   `json:"code,omitempty"`
	Detail  string   `json:"detail,omitempty"`
	Files   []string `json:"files,omitempty"`
	Bytes   int64    `json:"bytes,omitempty"`
	SHA256  string   `json:"sha256,omitempty"`
	Matches []string `json:"matches,omitempty"`
}

// CheckResult is the outcome of the node's check command.
type CheckResult struct {
	Command    string `json:"command"`
	OK         bool   `json:"ok"`
	ExitCode   int    `json:"exit_code"`
	Output     string `json:"output,omitempty"`
	DurationMS int64  `json:"duration_ms"`
	TimedOut   bool   `json:"timed_out,omitempty"`
}

// Report is the full verdict for one node.
type Report struct {
	NodeID    string           `json:"node"`
	OK        bool             `json:"ok"`
	Artifacts []ArtifactResult `json:"artifacts,omitempty"`
	Check     *CheckResult     `json:"check,omitempty"`
	Reason    string           `json:"reason,omitempty"`
}

// Node verifies a node against the filesystem rooted at root.
func Node(ctx context.Context, root string, n *plan.Node) Report {
	r := Report{NodeID: n.ID, OK: true}
	var failures []string

	for _, a := range n.Artifacts {
		res := checkArtifact(root, a)
		r.Artifacts = append(r.Artifacts, res)
		if !res.OK {
			r.OK = false
			failures = append(failures, fmt.Sprintf("%s: %s %s", a.Path, res.Code, res.Detail))
		}
	}

	if n.Check != "" {
		dir := root
		if n.Workdir != "" {
			dir = filepath.Join(root, n.Workdir)
		}
		c := runCheck(ctx, dir, n.Check, n.TimeoutDuration())
		r.Check = &c
		if !c.OK {
			r.OK = false
			failures = append(failures, fmt.Sprintf("check exited %d", c.ExitCode))
		}
	}

	if !r.OK {
		r.Reason = strings.Join(failures, "; ")
	}
	return r
}

// Satisfied is the cheap form used by the scheduler: a node whose artifacts
// already exist is not re-run, which is also the duplicate-work guard.
func Satisfied(ctx context.Context, root string, n *plan.Node) bool {
	if len(n.Artifacts) == 0 && n.Check == "" {
		return false
	}
	return Node(ctx, root, n).OK
}

func checkArtifact(root string, a plan.Artifact) ArtifactResult {
	res := ArtifactResult{Path: a.Path}
	target := a.Path
	if !filepath.IsAbs(target) {
		target = filepath.Join(root, a.Path)
	}

	var files []string
	if strings.ContainsAny(a.Path, "*?[") {
		matches, err := filepath.Glob(target)
		if err != nil {
			res.Code, res.Detail = "bad_glob", err.Error()
			return res
		}
		files = matches
	} else {
		if _, err := os.Stat(target); err != nil {
			res.Code = "missing"
			res.Detail = "file does not exist"
			return res
		}
		files = []string{target}
	}

	need := a.MinCount
	if need <= 0 {
		need = 1
	}
	if len(files) < need {
		res.Code = "count"
		res.Detail = fmt.Sprintf("%d match(es), need %d", len(files), need)
		return res
	}

	var total int64
	var combined strings.Builder
	var hashes []string
	for _, f := range files {
		info, err := os.Stat(f)
		if err != nil {
			res.Code, res.Detail = "stat", err.Error()
			return res
		}
		if info.IsDir() {
			res.Code, res.Detail = "is_dir", f+" is a directory"
			return res
		}
		total += info.Size()
		data, err := os.ReadFile(f)
		if err != nil {
			res.Code, res.Detail = "read", err.Error()
			return res
		}
		combined.Write(data)
		sum := sha256.Sum256(data)
		hashes = append(hashes, hex.EncodeToString(sum[:]))
		if len(files) == 1 {
			res.SHA256 = hex.EncodeToString(sum[:])
		}
	}
	res.Files = relAll(root, files)
	res.Bytes = total

	if total == 0 {
		res.Code = "empty"
		res.Detail = "artifact is zero bytes"
		return res
	}
	if a.MinBytes > 0 && total < int64(a.MinBytes) {
		res.Code = "too_small"
		res.Detail = fmt.Sprintf("%d bytes, need %d", total, a.MinBytes)
		return res
	}

	body := combined.String()
	for _, want := range a.MustContain {
		if !strings.Contains(body, want) {
			res.Code = "missing_text"
			res.Detail = fmt.Sprintf("does not contain %q", want)
			return res
		}
		res.Matches = append(res.Matches, want)
	}
	if a.MustMatch != "" {
		re, err := regexp.Compile(a.MustMatch)
		if err != nil {
			res.Code, res.Detail = "bad_regex", err.Error()
			return res
		}
		if !re.MatchString(body) {
			res.Code = "no_match"
			res.Detail = fmt.Sprintf("does not match %q", a.MustMatch)
			return res
		}
	}
	if len(hashes) > 1 {
		res.SHA256 = fmt.Sprintf("%d files", len(hashes))
	}
	res.OK = true
	return res
}

func relAll(root string, files []string) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		if rel, err := filepath.Rel(root, f); err == nil && !strings.HasPrefix(rel, "..") {
			out = append(out, rel)
		} else {
			out = append(out, f)
		}
	}
	return out
}

func runCheck(ctx context.Context, dir, command string, timeout time.Duration) CheckResult {
	start := time.Now()
	c := CheckResult{Command: command}

	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, "sh", "-c", command)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	// kill the whole process group so a check that spawns children cannot
	// outlive its timeout
	setProcessGroup(cmd)

	out, err := cmd.CombinedOutput()
	c.DurationMS = time.Since(start).Milliseconds()
	if cctx.Err() == context.DeadlineExceeded {
		c.TimedOut = true
		c.ExitCode = -1
		c.Output = truncate(string(out), 4000)
		return c
	}
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			c.ExitCode = ee.ExitCode()
		} else {
			c.ExitCode = -1
		}
		c.Output = truncate(string(out), 4000)
		return c
	}
	c.ExitCode = 0
	c.OK = true
	c.Output = truncate(string(out), 4000)
	return c
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n... (truncated)"
}

// Summary renders a one-line verdict.
func (r Report) Summary() string {
	if r.OK {
		return "ok"
	}
	return r.Reason
}
