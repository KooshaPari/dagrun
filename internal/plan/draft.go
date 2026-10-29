package plan

import (
	"fmt"
	"regexp"
	"strings"
)

// cmdPattern finds a shell command embedded in an intent: a backticked span,
// or a line that starts with a command verb.
var cmdPattern = regexp.MustCompile("`([^`]{3,200})`")

// cmdVerbs are the verbs that make a line look like a runnable command.
var cmdVerbs = []string{
	"run ", "test ", "build ", "make ", "cargo ", "go test", "go build",
	"npm ", "pnpm ", "yarn ", "pytest", "make ", "just ", "dagrun ",
}

// Draft builds a runnable plan from one paragraph of intent.
//
// The skeleton is deliberately complete rather than clever: an intent, a spec, a
// test, an implementation, and a gate that has to pass. That is the smallest
// set a reader can check, and it guarantees the emitted plan validates.
//
// The gate's check is taken from the intent when the intent names a command,
// because a gate that does not run the thing you asked for is theatre.
func Draft(intent, runName string) (*Plan, error) {
	if strings.TrimSpace(intent) == "" {
		return nil, fmt.Errorf("intent is empty")
	}
	if strings.TrimSpace(runName) == "" {
		runName = "run"
	}
	check := extractCheck(intent)
	if check == "" {
		check = "true"
	}

	p := &Plan{
		Version:       1,
		Run:           runName,
		Intent:        strings.TrimSpace(intent),
		ArtifactsRoot: "artifacts",
		MaxParallel:   4,
		Nodes: []*Node{
			{
				ID: "1.1", Kind: "INTENT", Title: "Freeze the intent and the acceptance criteria",
				Estimate: "10m", Role: "intent",
				Artifacts: []Artifact{{
					Path: "artifacts/INTENT.md", MinBytes: 300,
					MustContain: []string{"Acceptance Criteria"},
				}},
			},
			{
				ID: "1.2", Kind: "SPEC", Title: "Write the behavioural contract",
				Deps: []string{"1.1"}, Estimate: "10m", Role: "spec",
				Artifacts: []Artifact{{
					Path: "artifacts/SPEC.md", MinBytes: 400,
					MustContain: []string{"Requirement"},
				}},
			},
			{
				ID: "2.1", Kind: "TEST", Title: "Write the failing test first",
				Deps: []string{"1.2"}, Estimate: "10m", Role: "impl",
				Artifacts: []Artifact{{
					Path: "artifacts/TEST.md", MinBytes: 200,
					MustContain: []string{"Test"},
				}},
			},
			{
				ID: "2.2", Kind: "IMPL", Title: "Implement it",
				Deps: []string{"2.1"}, Estimate: "10m", Role: "impl",
				Artifacts: []Artifact{{
					Path: "artifacts/IMPLEMENTATION.md", MinBytes: 200,
				}},
			},
			{
				ID: "3.1", Kind: "GATE", Title: "The gate must pass",
				Deps: []string{"2.2"}, Estimate: "5m", Role: "gate",
				Check: check,
			},
		},
	}
	p.normalize()
	return p, nil
}

// DraftYAML builds a plan and renders it as YAML.
func DraftYAML(intent, runName string) ([]byte, error) {
	p, err := Draft(intent, runName)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	b.WriteString("version: 1\n")
	fmt.Fprintf(&b, "run: %s\n", p.Run)
	b.WriteString("intent: |\n")
	for _, line := range strings.Split(strings.TrimRight(p.Intent, "\n"), "\n") {
		fmt.Fprintf(&b, "  %s\n", strings.TrimRight(line, " "))
	}
	b.WriteString("artifacts_root: artifacts\n")
	b.WriteString("max_parallel: 4\n")
	b.WriteString("defaults:\n")
	b.WriteString("  estimate: 10m\n")
	b.WriteString("  timeout: 30m\n")
	b.WriteString("  max_attempts: 2\n")
	b.WriteString("nodes:\n")
	for _, n := range p.Nodes {
		fmt.Fprintf(&b, "  - id: %q\n", n.ID)
		fmt.Fprintf(&b, "    kind: %s\n", n.Kind)
		fmt.Fprintf(&b, "    title: %q\n", n.Title)
		if len(n.Deps) > 0 {
			b.WriteString("    deps:\n")
			for _, d := range n.Deps {
				fmt.Fprintf(&b, "      - %q\n", d)
			}
		}
		fmt.Fprintf(&b, "    role: %s\n", n.Role)
		if n.Check != "" {
			fmt.Fprintf(&b, "    check: %q\n", n.Check)
		}
		if len(n.Artifacts) > 0 {
			b.WriteString("    artifacts:\n")
			for _, a := range n.Artifacts {
				fmt.Fprintf(&b, "      - path: %q\n", a.Path)
				if a.MinBytes > 0 {
					fmt.Fprintf(&b, "        min_bytes: %d\n", a.MinBytes)
				}
				if len(a.MustContain) > 0 {
					b.WriteString("        must_contain:\n")
					for _, m := range a.MustContain {
						fmt.Fprintf(&b, "          - %q\n", m)
					}
				}
			}
		}
	}
	return []byte(b.String()), nil
}

// extractCheck finds a runnable command in the intent.
func extractCheck(intent string) string {
	if m := cmdPattern.FindStringSubmatch(intent); len(m) == 2 {
		if looksLikeCommand(m[1]) {
			return strings.TrimSpace(m[1])
		}
	}
	for _, line := range strings.Split(intent, "\n") {
		l := strings.TrimSpace(line)
		l = strings.TrimLeft(l, "-*0123456789. ")
		lower := strings.ToLower(l)
		for _, v := range cmdVerbs {
			if strings.HasPrefix(lower, v) {
				return strings.TrimSpace(l)
			}
		}
	}
	return ""
}

// looksLikeCommand rejects prose that merely sits between backticks.
func looksLikeCommand(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	// A command has no spaces before its first argument, or starts with a known
	// tool. Prose like "the `store` package" has spaces.
	first := s
	if i := strings.IndexByte(s, ' '); i > 0 {
		first = s[:i]
	}
	if strings.ContainsAny(first, ".,;:") {
		return false
	}
	lower := strings.ToLower(s)
	for _, v := range cmdVerbs {
		if strings.HasPrefix(lower, v) {
			return true
		}
	}
	// A path or a flag-led invocation also reads as a command.
	return strings.HasPrefix(s, "./") || strings.HasPrefix(s, "/") || strings.HasPrefix(s, "-")
}
