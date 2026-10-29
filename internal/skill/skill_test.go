package skill

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCommandsNonEmptyAndComplete(t *testing.T) {
	cmds := Commands()
	if len(cmds) == 0 {
		t.Fatal("Commands() returned no commands")
	}

	seen := make(map[string]bool, len(cmds))
	for _, c := range cmds {
		if c.Name == "" {
			t.Errorf("command with empty name: %+v", c)
			continue
		}
		if seen[c.Name] {
			t.Errorf("duplicate command %q", c.Name)
		}
		seen[c.Name] = true

		if strings.TrimSpace(c.Purpose) == "" {
			t.Errorf("%q has no purpose", c.Name)
		}
		if strings.TrimSpace(c.Example) == "" {
			t.Errorf("%q has no example", c.Name)
		}
		// An example is only useful if it is the command it documents.
		if !strings.Contains(c.Example, "dagrun "+c.Name) {
			t.Errorf("%q example does not invoke dagrun %s: %q", c.Name, c.Name, c.Example)
		}
	}

	for _, want := range []string{
		"plan", "next", "run", "claim", "done", "explain", "status", "validate",
	} {
		if !seen[want] {
			t.Errorf("canonical command %q missing from Commands()", want)
		}
	}
}

func TestCommandsReturnsCopy(t *testing.T) {
	cmds := Commands()
	if len(cmds) == 0 {
		t.Fatal("Commands() returned no commands")
	}
	cmds[0].Name = "mutated"
	if got := Commands()[0].Name; got == "mutated" {
		t.Fatal("Commands() exposes the shared table; a caller mutated it")
	}
}

func TestRenderMentionsEveryCommand(t *testing.T) {
	out := Render()
	if !strings.Contains(out, "| command |") {
		t.Errorf("Render() has no table header:\n%s", out)
	}
	for _, c := range Commands() {
		if !strings.Contains(out, c.Name) {
			t.Errorf("Render() does not mention %q", c.Name)
		}
		if !strings.Contains(out, c.Example) {
			t.Errorf("Render() does not show the example for %q", c.Name)
		}
	}
}

func TestSkillMarkdownHasFrontmatterAndKeyPhrases(t *testing.T) {
	md := SkillMarkdown()

	if !strings.HasPrefix(md, "---\n") {
		t.Fatal("SkillMarkdown() does not start with YAML frontmatter")
	}
	if !strings.Contains(md, "\nname: dagrun\n") {
		t.Error("frontmatter has no name: dagrun")
	}
	if !strings.Contains(md, "description: ") ||
		!strings.Contains(md, "Plan and execute a multi-step task as a verified DAG") {
		t.Error("frontmatter description is missing or does not say when to use the tool")
	}

	for _, want := range []string{
		"## Commands",
		"## Plan format",
		"artifacts:",
		"must_contain",
		"A node is done only when its artifacts verify",
		"## Common mistakes",
		"dagrun done NODE",
		"parallel nodes",
		"check of `true`",
		"cannot fail",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("SkillMarkdown() missing %q", want)
		}
	}

	// The table is rendered, not copied, so drift between prose and table is
	// impossible. Assert the composition explicitly.
	if !strings.Contains(md, Render()) {
		t.Error("SkillMarkdown() does not embed Render() verbatim")
	}

	for _, c := range Commands() {
		if !strings.Contains(md, c.Name) {
			t.Errorf("SkillMarkdown() does not mention %q", c.Name)
		}
	}
}

// TestShippedSkillMatchesRenderer keeps skills/dagrun/SKILL.md honest: the file
// agents read is the renderer's output, not a hand-edited fork of it. The
// skill must stay under 150 lines because it is read into agent context.
func TestShippedSkillMatchesRenderer(t *testing.T) {
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Skip("cannot locate source file")
	}
	path := filepath.Join(filepath.Dir(self), "..", "..", "skills", "dagrun", "SKILL.md")

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("shipped skill not readable: %v", err)
	}

	lines := strings.Count(string(raw), "\n")
	if lines >= 150 {
		t.Errorf("skills/dagrun/SKILL.md is %d lines; keep it under 150", lines)
	}

	if got, want := string(raw), SkillMarkdown(); got != want {
		t.Errorf("skills/dagrun/SKILL.md has drifted from SkillMarkdown()\n"+
			"regenerate it from internal/skill instead of editing the markdown by hand\n"+
			"(file %d bytes, renderer %d bytes)", len(got), len(want))
	}
}
