package diffscope

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestMatches(t *testing.T) {
	cases := []struct {
		pattern string
		path    string
		want    bool
	}{
		{"src/a.go", "src/a.go", true},
		{"src/a.go", "src/b.go", false},
		{"src/*.go", "src/a.go", true},
		{"src/*.go", "src/a/b.go", false}, // "*" stays inside one segment
		{"src/**/*.go", "src/a/b.go", true},
		{"src/*.go", "docs/a.go", false},
		{"src/**/*.go", "src/a/b/c.go", true},
		{"src/**/*.go", "src/a.go", true},
		{"internal/**/*.go", "internal/pkg/x.go", true},
		{"internal/**/*.go", "internal/pkg/x_test.go", true},
		{"**/*.go", "a/b/c.go", true},
		{"*.go", "a.go", true},
		{"*.go", "a/b.go", false},
		{"", "a.go", false},
	}
	for _, c := range cases {
		if got := Matches(c.pattern, c.path); got != c.want {
			t.Errorf("Matches(%q, %q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}

func TestTouches(t *testing.T) {
	changes := []Change{
		{Path: "src/a.go", Status: "M"},
		{Path: "docs/readme.md", Status: "A"},
	}
	if !Touches(changes, []string{"src/*.go"}) {
		t.Error("src/a.go is in scope")
	}
	if Touches(changes, []string{"internal/*.go"}) {
		t.Error("nothing is in internal/")
	}
	if !Touches(changes, []string{"docs/*.md"}) {
		t.Error("docs/readme.md is in scope")
	}
}

func TestDiffReportsChangedPaths(t *testing.T) {
	repo := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "t@example.com")
	run("config", "user.name", "t")
	if err := os.MkdirAll(filepath.Join(repo, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "src", "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "base")
	base := gitHead(t, repo)

	run("checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(repo, "src", "a.go"), []byte("package a\n// changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "src", "b.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "work")
	ref := gitHead(t, repo)

	changes, err := Diff(repo, base, ref)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 {
		t.Fatalf("got %d changes, want 2: %+v", len(changes), changes)
	}
	if !Touches(changes, []string{"src/*.go"}) {
		t.Errorf("the change should be in scope: %+v", changes)
	}
	if Touches(changes, []string{"docs/*.md"}) {
		t.Errorf("the change should not be in scope: %+v", changes)
	}
}

func gitHead(t *testing.T, repo string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out[:40])
}
