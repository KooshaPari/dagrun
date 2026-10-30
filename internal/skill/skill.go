// Package skill holds the agent-facing description of dagrun in one place.
//
// A harness (jcode, kcode, codex, forge, helioslite) learns to use this tool
// from a single markdown skill. The CLI is the same everywhere, so the skill is
// the same everywhere too: one file, no per-harness variants. Keeping the
// command list as data and rendering both the table and the prose from it means
// the skill cannot drift from itself when a command is added or reworded.
package skill

import (
	"fmt"
	"strings"
)

// Command is one CLI verb as an agent needs to see it.
type Command struct {
	Name    string // verb, e.g. "done"
	Args    string // positional arguments, e.g. "NODE"
	Purpose string // one line: when to reach for it
	Example string // one runnable invocation
}

// commands is the canonical list: the verbs that carry the loop, plus the ones
// an agent needs to close it out. The loop is plan -> next -> claim -> done;
// trace says what happened, submit hands the run to Tracera, and skill is how
// an agent loads this table without running --help.
//
// The remaining verbs (export, viz, lint, critique, review, doctor, gc,
// workspace, timeline, stats, draft, template, detect) are inspection and
// authoring helpers. They are documented by --help and deliberately left out
// here: a skill that lists everything teaches nothing about where to start.
var commands = []Command{
	{
		Name:    "plan",
		Args:    "[FILE]",
		Purpose: "validate a plan and install it as the run",
		Example: "dagrun plan plan.yaml",
	},
	{
		Name:    "next",
		Args:    "",
		Purpose: "list the ready nodes, with the prompt to hand an agent",
		Example: "dagrun next --json",
	},
	{
		Name:    "run",
		Args:    "",
		Purpose: "execute ready nodes by spawning agent harnesses",
		Example: "dagrun run --workers 4",
	},
	{
		Name:    "claim",
		Args:    "NODE",
		Purpose: "take one node when you are the agent doing the work",
		Example: "dagrun claim 1.2 --owner me",
	},
	{
		Name:    "done",
		Args:    "NODE",
		Purpose: "verify the node's artifacts, then record it as done",
		Example: "dagrun done 1.2",
	},
	{
		Name:    "explain",
		Args:    "[NODE]",
		Purpose: "show why a node is ready, blocked, or failed",
		Example: "dagrun explain 1.2",
	},
	{
		Name:    "status",
		Args:    "",
		Purpose: "show the tree and the progress through it",
		Example: "dagrun status",
	},
	{
		Name:    "validate",
		Args:    "",
		Purpose: "re-check every node against the filesystem",
		Example: "dagrun validate",
	},
	{
		Name:    "trace",
		Args:    "",
		Purpose: "the merged execution trace: what happened, as opposed to what the state file says",
		Example: "dagrun trace --json",
	},
	{
		Name:    "submit",
		Args:    "",
		Purpose: "post the run to a Tracera instance; fails loud on any reported error",
		Example: "dagrun submit --url http://localhost:3180 --token TRACERA_TOKEN",
	},
	{
		Name:    "skill",
		Args:    "",
		Purpose: "print this skill, so an agent can load it without running --help",
		Example: "dagrun skill",
	},
}

// Commands returns the canonical verb list. The caller gets a copy so a
// harness adapter cannot mutate the shared table.
func Commands() []Command {
	out := make([]Command, len(commands))
	copy(out, commands)
	return out
}

// signature renders the invocable form, e.g. "done NODE".
func (c Command) signature() string {
	if c.Args == "" {
		return c.Name
	}
	return c.Name + " " + c.Args
}

// Render returns a compact markdown table of the commands. It is used verbatim
// inside the skill, so an agent reads the same rows the tests check.
func Render() string {
	var b strings.Builder
	b.WriteString("| command | what it does | example |\n")
	b.WriteString("| --- | --- | --- |\n")
	for _, c := range commands {
		fmt.Fprintf(&b, "| `%s` | %s | `%s` |\n", c.signature(), c.Purpose, c.Example)
	}
	return b.String()
}

// SkillMarkdown returns the whole skill: YAML frontmatter plus the body. The
// command table comes from Render, so the prose and the table share one source.
func SkillMarkdown() string {
	var b strings.Builder

	b.WriteString("---\n")
	b.WriteString("name: dagrun\n")
	b.WriteString("description: Plan and execute a multi-step task as a verified DAG, " +
		"where a node is done only when its declared artifacts verify.\n")
	b.WriteString("---\n\n")

	b.WriteString("# dagrun\n\n")
	b.WriteString("dagrun is a work-DAG executor for coding agents. You write a plan of nodes, " +
		"each naming the artifacts that prove it happened; dagrun decides what is ready, " +
		"hands you or a spawned agent the prompt for it, verifies the artifacts, and records " +
		"the result. The DAG is the trace, so there is no second task list to drift out of sync.\n\n")

	b.WriteString("## Commands\n\n")
	b.WriteString(Render())
	b.WriteString("\n")

	b.WriteString("## Plan format\n\n")
	b.WriteString("```yaml\n")
	b.WriteString("version: 1\n")
	b.WriteString("run: my-run\n")
	b.WriteString("artifacts_root: artifacts\n")
	b.WriteString("max_parallel: 4\n")
	b.WriteString("nodes:\n")
	b.WriteString("  - id: \"1.1\"\n")
	b.WriteString("    title: Freeze the scope\n")
	b.WriteString("    check: \"test -s artifacts/INTENT.md\"   # optional, must be able to fail\n")
	b.WriteString("    artifacts:\n")
	b.WriteString("      - path: artifacts/INTENT.md\n")
	b.WriteString("        min_bytes: 400\n")
	b.WriteString("        must_contain: [\"Acceptance Criteria\"]\n")
	b.WriteString("  - id: \"1.2\"\n")
	b.WriteString("    deps: [\"1.1\"]\n")
	b.WriteString("    title: Implement the thing\n")
	b.WriteString("    artifacts:\n")
	b.WriteString("      - path: artifacts/impl.md\n")
	b.WriteString("```\n\n")

	b.WriteString("## The one rule\n\n")
	b.WriteString("A node is done only when its artifacts verify. `dagrun done NODE` checks the " +
		"declared paths, sizes, and `must_contain` strings against the filesystem; if they do " +
		"not hold it refuses and says which contract failed. To finish a node, satisfy its " +
		"contract and call the command. Do not report completion any other way.\n\n")

	b.WriteString("## Common mistakes\n\n")
	b.WriteString("- Writing the artifacts and moving on instead of calling `dagrun done NODE`. " +
		"The node stays in progress and its dependents never become ready.\n")
	b.WriteString("- Giving two parallel nodes the same artifact path. Both write it, the last " +
		"writer wins, and one node verifies a file the other authored. One artifact, one node.\n")
	b.WriteString("- Declaring a check of `true`. It cannot fail, so it proves nothing; leave " +
		"`check` out or make it assert something real.\n")
	b.WriteString("- Using a command that cannot fail (`ls`, `echo`, `cat`), or one whose exit " +
		"status you then ignore. A check that always passes is not a check.\n")
	b.WriteString("- Treating `dagrun run` as the only path. When you are already the agent, " +
		"`claim`, do the work, `done` is the loop.\n")

	return b.String()
}
