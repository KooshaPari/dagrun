package verify

import (
	"strings"
	"testing"

	"github.com/KooshaPari/dagrun/internal/plan"
)

func TestHintTable(t *testing.T) {
	node := &plan.Node{
		ID: "1.2",
		Artifacts: []plan.Artifact{
			{Path: "artifacts/code.go", MinBytes: 200, MustContain: []string{"func main"}, MustMatch: "^package ", MinCount: 3},
		},
	}

	cases := []struct {
		name   string
		report Report
		want   []string // substrings, all must appear
	}{
		{
			name:   "missing",
			report: Report{Artifacts: []ArtifactResult{{Path: "artifacts/code.go", Code: "missing", Detail: "file does not exist"}}},
			want:   []string{"create artifacts/code.go", "dagrun done 1.2"},
		},
		{
			name:   "empty",
			report: Report{Artifacts: []ArtifactResult{{Path: "artifacts/code.go", Code: "empty", Detail: "artifact is zero bytes"}}},
			want:   []string{"zero bytes", "allow_empty", "dagrun done 1.2"},
		},
		{
			name:   "too_small quotes the declared floor",
			report: Report{Artifacts: []ArtifactResult{{Path: "artifacts/code.go", Code: "too_small", Detail: "12 bytes, need 200", Bytes: 12}}},
			want:   []string{"at least 200 bytes", "currently 12", "dagrun done 1.2"},
		},
		{
			name:   "missing_text quotes the observed detail",
			report: Report{Artifacts: []ArtifactResult{{Path: "artifacts/code.go", Code: "missing_text", Detail: `does not contain "func main"`}}},
			want:   []string{"write the required text into artifacts/code.go", `does not contain "func main"`, "dagrun done 1.2"},
		},
		{
			name:   "no_match quotes the regex",
			report: Report{Artifacts: []ArtifactResult{{Path: "artifacts/code.go", Code: "no_match", Detail: `does not match "^package "`}}},
			want:   []string{"match regex", `^package `, "dagrun done 1.2"},
		},
		{
			name:   "count quotes the declared minimum",
			report: Report{Artifacts: []ArtifactResult{{Path: "artifacts/code.go", Code: "count", Detail: "1 match(es), need 3"}}},
			want:   []string{"at least 3 path(s)", "1 match(es), need 3", "dagrun done 1.2"},
		},
		{
			name:   "is_dir",
			report: Report{Artifacts: []ArtifactResult{{Path: "artifacts/code.go", Code: "is_dir", Detail: "is a directory"}}},
			want:   []string{"replace the directory", "regular file", "dagrun done 1.2"},
		},
		{
			name:   "bad_regex sends you back to the plan",
			report: Report{Artifacts: []ArtifactResult{{Path: "artifacts/code.go", Code: "bad_regex", Detail: "error parsing regexp"}}},
			want:   []string{"fix the plan", "must_match", "dagrun plan FILE"},
		},
		{
			name:   "bad_glob sends you back to the plan",
			report: Report{Artifacts: []ArtifactResult{{Path: "artifacts/*.go", Code: "bad_glob", Detail: "syntax error in pattern"}}},
			want:   []string{"fix the plan", "glob", "dagrun plan FILE"},
		},
		{
			name:   "unmapped code still points at the evidence",
			report: Report{Artifacts: []ArtifactResult{{Path: "artifacts/code.go", Code: "read", Detail: "permission denied"}}},
			want:   []string{"inspect artifacts/code.go", "permission denied"},
		},
		{
			name:   "first failure wins, later results are ignored",
			report: Report{Artifacts: []ArtifactResult{{Path: "artifacts/code.go", Code: "missing", Detail: "file does not exist"}, {Path: "artifacts/code.go", OK: true}}},
			want:   []string{"create artifacts/code.go"},
		},
		{
			name:   "passing report has nothing to fix",
			report: Report{OK: true, Artifacts: []ArtifactResult{{Path: "artifacts/code.go", OK: true}}},
			want:   []string{},
		},
		{
			name:   "failing check",
			report: Report{Check: &CheckResult{Command: "go test ./...", OK: false, ExitCode: 2, Output: "FAIL\tpkg\t0.1s\nmore"}},
			want:   []string{"make the check pass", "go test ./...", "exited 2", "FAIL", "dagrun done 1.2"},
		},
		{
			name:   "timed out check",
			report: Report{Check: &CheckResult{Command: "sleep 999", OK: false, ExitCode: -1, TimedOut: true}},
			want:   []string{"timed out", "sleep 999", "dagrun done 1.2"},
		},
		{
			name:   "no artifacts and no check yields no hint",
			report: Report{Artifacts: nil},
			want:   []string{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Hint(node, tc.report)
			if len(tc.want) == 0 {
				if got != "" {
					t.Fatalf("Hint = %q, want empty", got)
				}
				return
			}
			if got == "" {
				t.Fatalf("Hint returned empty, want substrings %v", tc.want)
			}
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("Hint = %q; missing %q", got, w)
				}
			}
			if strings.ContainsAny(got, "\n\r\t") {
				t.Errorf("Hint must be one line, got %q", got)
			}
		})
	}
}

func TestHintNilNodeIsSafe(t *testing.T) {
	got := Hint(nil, Report{Artifacts: []ArtifactResult{{Path: "x", Code: "missing", Detail: "file does not exist"}}})
	if !strings.Contains(got, "create x") {
		t.Fatalf("Hint(nil, ...) = %q, want an instruction naming the path", got)
	}
	if strings.ContainsAny(got, "\n\r\t") {
		t.Fatalf("Hint must be one line, got %q", got)
	}
}
