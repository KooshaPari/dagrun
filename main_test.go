package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Every command must exit 0 on --help.
//
// The defect this guards against was central rather than per-command:
// flag.ContinueOnError returns flag.ErrHelp, and main propagated every
// non-nil error to os.Exit(1). So `dagrun plan --help` printed a perfectly
// good usage block and then exited 1. 24 of the 26 commands were affected,
// because only the few that handle --help themselves were correct.
//
// An agent's first move against an unfamiliar CLI is to ask it for help.
// Making that look like a failure trains the agent to distrust the tool's
// output or to retry blindly, so the exit code matters as much as the text.

// allCommands is the dispatch switch, minus the ones that are not commands.
var allCommands = []string{
	"plan", "run", "next", "claim", "done", "fail", "skip", "status",
	"validate", "trace", "export", "submit", "detect", "mcp", "explain",
	"viz", "draft", "doctor", "gc", "workspace", "timeline", "stats",
	"critique", "lint", "review", "template", "skill",
}

// buildCLI compiles the binary once for the whole test.
func buildCLI(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "dagrun")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

func TestEveryCommandExitsZeroOnHelp(t *testing.T) {
	bin := buildCLI(t)
	dir := t.TempDir()
	for _, c := range allCommands {
		t.Run(c, func(t *testing.T) {
			cmd := exec.Command(bin, c, "--help")
			cmd.Dir = dir
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("`dagrun %s --help` exited non-zero: %v\n%s", c, err, out)
			}
			if len(strings.TrimSpace(string(out))) == 0 {
				t.Errorf("`dagrun %s --help` printed nothing", c)
			}
		})
	}
}

func TestNoArgsPrintsUsageAndExitsTwo(t *testing.T) {
	bin := buildCLI(t)
	cmd := exec.Command(bin)
	cmd.Dir = t.TempDir()
	out, _ := cmd.CombinedOutput()
	if code := cmd.ProcessState.ExitCode(); code != 2 {
		t.Fatalf("no-args exit = %d, want 2\n%s", code, out)
	}
	if !strings.Contains(string(out), "dagrun - a work-DAG executor") {
		t.Errorf("no-args output does not contain the usage banner:\n%s", out)
	}
}

func TestUnknownCommandExitsTwo(t *testing.T) {
	bin := buildCLI(t)
	cmd := exec.Command(bin, "definitely-not-a-command")
	cmd.Dir = t.TempDir()
	out, _ := cmd.CombinedOutput()
	if code := cmd.ProcessState.ExitCode(); code != 2 {
		t.Fatalf("unknown-command exit = %d, want 2\n%s", code, out)
	}
	if !strings.Contains(string(out), "unknown command") {
		t.Errorf("unknown-command output lacks the reason:\n%s", out)
	}
}

// Every command in the dispatch switch must be documented in usage(), and
// every command in usage() must exist. A command that exists but is
// undocumented is a command an agent cannot find; a documented command that
// does not exist is worse, because the agent tries it and fails.
func TestUsageAndDispatchAgree(t *testing.T) {
	bin := buildCLI(t)
	cmd := exec.Command(bin)
	cmd.Dir = t.TempDir()
	out, _ := cmd.CombinedOutput()
	text := string(out)

	documented := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "dagrun" {
			continue
		}
		name := strings.TrimPrefix(fields[1], "-")
		// The banner line is "dagrun - a work-DAG executor ..."; the dash
		// there is prose, not a command.
		if name == "-" || name == "" {
			continue
		}
		documented[name] = true
	}

	for _, c := range allCommands {
		if !documented[c] {
			t.Errorf("command %q exists but is not in usage()", c)
		}
	}
	for name := range documented {
		found := false
		for _, c := range allCommands {
			if c == name {
				found = true
				break
			}
		}
		if !found && name != "help" && name != "version" {
			t.Errorf("usage() documents %q but no such command exists", name)
		}
	}
}

// The skill is the agent-facing documentation. It must be reachable, and it
// must be valid frontmatter plus a command table.
func TestSkillCommandPrintsFrontmatterAndTable(t *testing.T) {
	bin := buildCLI(t)
	cmd := exec.Command(bin, "skill")
	cmd.Dir = t.TempDir()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("skill exited non-zero: %v\n%s", err, out)
	}
	text := string(out)
	if !strings.HasPrefix(text, "---\n") {
		t.Errorf("skill does not start with YAML frontmatter:\n%s", text)
	}
	if !strings.Contains(text, "name: dagrun") {
		t.Errorf("skill frontmatter lacks the name:\n%s", text)
	}
	if !strings.Contains(text, "| command | what it does | example |") {
		t.Errorf("skill lacks the command table:\n%s", text)
	}
}

// The token must never be passed by value. The help text has to say so, or an
// agent will put a literal secret on the command line where it lands in shell
// history and in ps output.
func TestSubmitHelpDocumentsTheTokenAsAnEnvVarName(t *testing.T) {
	bin := buildCLI(t)
	cmd := exec.Command(bin, "submit", "--help")
	cmd.Dir = t.TempDir()
	out, _ := cmd.CombinedOutput()
	text := string(out)
	if !strings.Contains(text, "env var") {
		t.Errorf("submit --help does not mention the env var:\n%s", text)
	}
	// The flag must be described as the NAME of a variable, not as a token.
	if !strings.Contains(text, "name of the env var") {
		t.Errorf("submit --help does not say the flag takes a NAME:\n%s", text)
	}
}

func TestRunHelpDocumentsForceAndDryRun(t *testing.T) {
	bin := buildCLI(t)
	cmd := exec.Command(bin, "run", "--help")
	cmd.Dir = t.TempDir()
	out, _ := cmd.CombinedOutput()
	text := string(out)
	for _, want := range []string{"-force", "-dry-run"} {
		if !strings.Contains(text, want) {
			t.Errorf("run --help lacks %s:\n%s", want, text)
		}
	}
}
