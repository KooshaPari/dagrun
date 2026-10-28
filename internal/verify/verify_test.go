package verify

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/KooshaPari/dagrun/internal/plan"
)

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestArtifactPredicates(t *testing.T) {
	root := t.TempDir()
	write(t, root, "artifacts/doc.md", "hello world, this is a reasonably long body with Sources inside")

	cases := []struct {
		name string
		a    plan.Artifact
		ok   bool
		code string
	}{
		{"present", plan.Artifact{Path: "artifacts/doc.md"}, true, ""},
		{"missing", plan.Artifact{Path: "artifacts/nope.md"}, false, "missing"},
		{"too small", plan.Artifact{Path: "artifacts/doc.md", MinBytes: 10000}, false, "too_small"},
		{"must contain hit", plan.Artifact{Path: "artifacts/doc.md", MustContain: []string{"Sources"}}, true, ""},
		{"must contain miss", plan.Artifact{Path: "artifacts/doc.md", MustContain: []string{"Absent"}}, false, "missing_text"},
		{"regex hit", plan.Artifact{Path: "artifacts/doc.md", MustMatch: `h.llo`}, true, ""},
		{"regex miss", plan.Artifact{Path: "artifacts/doc.md", MustMatch: `^zzz`}, false, "no_match"},
		{"glob ok", plan.Artifact{Path: "artifacts/*.md"}, true, ""},
		{"glob count", plan.Artifact{Path: "artifacts/*.md", MinCount: 3}, false, "count"},
		{"dir rejected", plan.Artifact{Path: "artifacts"}, false, "is_dir"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n := &plan.Node{ID: "x", Kind: "DOC", Artifacts: []plan.Artifact{c.a}}
			rep := Node(context.Background(), root, n)
			if rep.OK != c.ok {
				t.Fatalf("ok = %v, want %v (reason %s)", rep.OK, c.ok, rep.Reason)
			}
			if !c.ok && c.code != "" && rep.Artifacts[0].Code != c.code {
				t.Fatalf("code = %q, want %q", rep.Artifacts[0].Code, c.code)
			}
		})
	}
}

func TestEmptyArtifactIsNotProof(t *testing.T) {
	root := t.TempDir()
	write(t, root, "artifacts/empty.md", "")
	n := &plan.Node{ID: "x", Kind: "DOC", Artifacts: []plan.Artifact{{Path: "artifacts/empty.md"}}}
	rep := Node(context.Background(), root, n)
	if rep.OK {
		t.Fatal("a zero-byte artifact must not count as done")
	}
	if rep.Artifacts[0].Code != "empty" {
		t.Fatalf("code = %q, want empty", rep.Artifacts[0].Code)
	}
}

func TestCheckExitCodes(t *testing.T) {
	root := t.TempDir()
	pass := &plan.Node{ID: "g1", Kind: "GATE", Check: "true"}
	fail := &plan.Node{ID: "g2", Kind: "GATE", Check: "false"}
	if rep := Node(context.Background(), root, pass); !rep.OK {
		t.Fatalf("expected pass, got %s", rep.Reason)
	}
	if rep := Node(context.Background(), root, fail); rep.OK {
		t.Fatal("expected fail")
	}
}

func TestCheckRunsInWorkdir(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, root, "sub/marker.txt", "here")
	n := &plan.Node{ID: "g", Kind: "GATE", Check: "test -f marker.txt", Workdir: "sub"}
	if rep := Node(context.Background(), root, n); !rep.OK {
		t.Fatalf("check did not run in workdir: %s", rep.Reason)
	}
}

func TestSatisfiedIsFalseWithoutAContract(t *testing.T) {
	n := &plan.Node{ID: "x", Kind: "OTHER"}
	if Satisfied(context.Background(), t.TempDir(), n) {
		t.Fatal("a node with no artifacts and no check cannot be satisfied")
	}
}

func TestSha256Recorded(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.txt", "abc")
	n := &plan.Node{ID: "x", Kind: "CODE", Artifacts: []plan.Artifact{{Path: "a.txt"}}}
	rep := Node(context.Background(), root, n)
	if !rep.OK {
		t.Fatal(rep.Reason)
	}
	const want = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if rep.Artifacts[0].SHA256 != want {
		t.Fatalf("sha = %s, want %s", rep.Artifacts[0].SHA256, want)
	}
}
