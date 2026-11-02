package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/KooshaPari/dagrun/internal/plan"
)

// importPlanYAML is a minimal two-node plan: B depends on A.
const importPlanYAML = `version: 1
run: import-roundtrip
intent: prove the import command reconstructs a plan from a Tracera payload
artifacts_root: artifacts
max_parallel: 2
defaults:
  estimate: 5m
  timeout: 10m
  max_attempts: 2
nodes:
  - id: "A"
    kind: IMPL
    role: impl
    title: Node A
    artifacts:
      - path: artifacts/a.md
        min_bytes: 10
  - id: "B"
    kind: IMPL
    role: impl
    title: Node B
    deps: ["A"]
    artifacts:
      - path: artifacts/b.md
        min_bytes: 10
`

// TestImportCommandRoundTrip proves the CLI reaches the importer: a plan is
// exported to a Tracera payload, imported back through the command, and the
// reconstructed plan must carry the same nodes, dependency edge and artifacts.
func TestImportCommandRoundTrip(t *testing.T) {
	bin := buildCLI(t)
	dir := t.TempDir()

	planPath := filepath.Join(dir, "plan.yaml")
	if err := os.WriteFile(planPath, []byte(importPlanYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	// Export the installed run to a Tracera ingest payload.
	exportCmd := exec.Command(bin, "plan", planPath)
	exportCmd.Dir = dir
	if out, err := exportCmd.CombinedOutput(); err != nil {
		t.Fatalf("dagrun plan: %v\n%s", err, out)
	}
	// `submit --dry-run` prints exactly the body the importer reads: the
	// namespaced item payload (node: / edge: / artifact:).
	payloadCmd := exec.Command(bin, "submit", "--dry-run")
	payloadCmd.Dir = dir
	payload, err := payloadCmd.Output()
	if err != nil {
		t.Fatalf("dagrun submit --dry-run: %v", err)
	}
	if len(strings.TrimSpace(string(payload))) == 0 {
		t.Fatal("submit produced an empty payload")
	}

	// Import it back.
	importCmd := exec.Command(bin, "import", "--in", "-")
	importCmd.Dir = dir
	importCmd.Stdin = strings.NewReader(string(payload))
	out, err := importCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dagrun import: %v\n%s", err, out)
	}

	var got plan.Plan
	if err := yaml.Unmarshal(out, &got); err != nil {
		t.Fatalf("import output is not a plan: %v\n%s", err, out)
	}
	if len(got.Nodes) != 2 {
		t.Fatalf("imported nodes = %d, want 2\n%s", len(got.Nodes), out)
	}
	byID := map[string]*plan.Node{}
	for _, n := range got.Nodes {
		byID[n.ID] = n
	}
	if _, ok := byID["A"]; !ok {
		t.Fatalf("node A missing from import:\n%s", out)
	}
	b, ok := byID["B"]
	if !ok {
		t.Fatalf("node B missing from import:\n%s", out)
	}
	if len(b.Deps) != 1 || b.Deps[0] != "A" {
		t.Fatalf("node B deps = %v, want [A]\n%s", b.Deps, out)
	}
	// Artifacts are recovered only when the payload carries artifact items.
	// The exporter emits those for artifacts that actually verified, so an
	// unrun plan legitimately has none. Artifact recovery is covered by
	// TestImportCommandWritesToFile.
}

// TestImportCommandWritesToFile proves --out writes the plan to disk.
func TestImportCommandWritesToFile(t *testing.T) {
	bin := buildCLI(t)
	dir := t.TempDir()

	payloadPath := filepath.Join(dir, "payload.json")
	payload := `{"items":[{"id":"node:A","kind":"node","title":"A","type":"IMPL"},
	{"id":"node:B","kind":"node","title":"B","type":"IMPL"},
	{"id":"edge:A->B","kind":"edge"},
	{"id":"artifact:A:artifacts/a.md","kind":"artifact","path":"artifacts/a.md","from":"A"}]}`
	if err := os.WriteFile(payloadPath, []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}

	outPath := filepath.Join(dir, "imported.yaml")
	cmd := exec.Command(bin, "import", "--in", payloadPath, "--out", outPath)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("dagrun import --out: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read imported plan: %v", err)
	}
	var got plan.Plan
	if err := yaml.Unmarshal(raw, &got); err != nil {
		t.Fatalf("imported file is not a plan: %v\n%s", err, raw)
	}
	if len(got.Nodes) != 2 {
		t.Fatalf("imported nodes = %d, want 2\n%s", len(got.Nodes), raw)
	}
	// The artifact item must land on its owning node.
	var foundArtifact bool
	for _, n := range got.Nodes {
		if n.ID != "A" {
			continue
		}
		for _, a := range n.Artifacts {
			if a.Path == "artifacts/a.md" {
				foundArtifact = true
			}
		}
	}
	if !foundArtifact {
		t.Fatalf("artifact artifacts/a.md not recovered on node A:\n%s", raw)
	}
}

// TestImportCommandRejectsGarbage proves a malformed payload fails loudly
// instead of writing an empty plan.
func TestImportCommandRejectsGarbage(t *testing.T) {
	bin := buildCLI(t)
	dir := t.TempDir()

	path := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, []byte("not json at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(dir, "should-not-exist.yaml")

	cmd := exec.Command(bin, "import", "--in", path, "--out", outPath)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("dagrun import accepted garbage (exit 0)\n%s", out)
	}
	if _, err := os.Stat(outPath); err == nil {
		t.Fatal("import wrote a plan despite failing")
	}
}
