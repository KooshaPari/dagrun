// Command dagrun is a work-DAG executor for coding agents.
//
// An agent writes a plan; dagrun decides what is ready, drives each node,
// verifies the artifacts the node promised, and records the result. The DAG is
// the trace, so there is no second system to drift out of sync.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/KooshaPari/dagrun/internal/plan"
	"github.com/KooshaPari/dagrun/internal/store"
)

// Version is set at build time with -ldflags "-X main.Version=...".
var Version = "1.0.0"

const defaultDir = ".dagrun"

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	var err error
	switch cmd {
	case "plan":
		err = cmdPlan(args)
	case "run":
		err = cmdRun(args)
	case "next":
		err = cmdNext(args)
	case "claim":
		err = cmdClaim(args)
	case "done":
		err = cmdDone(args)
	case "fail":
		err = cmdFail(args)
	case "skip":
		err = cmdSkip(args)
	case "status":
		err = cmdStatus(args)
	case "validate":
		err = cmdValidate(args)
	case "trace":
		err = cmdTrace(args)
	case "export":
		err = cmdExport(args)
	case "detect":
		err = cmdDetect(args)
	case "mcp":
		err = cmdMCP(args)
	case "explain":
		err = cmdExplain(args)
	case "viz":
		err = cmdViz(args)
	case "draft":
		err = cmdDraft(args)
	case "doctor":
		err = cmdDoctor(args)
	case "gc":
		err = cmdGC(args)
	case "workspace":
		err = cmdWorkspace(args)
	case "timeline":
		err = cmdTimeline(args)
	case "stats":
		err = cmdStats(args)
	case "critique":
		err = cmdCritique(args)
	case "lint":
		err = cmdLint(args)
	case "review":
		err = cmdReview(args)
	case "template":
		err = cmdTemplate(args)
	case "version", "--version", "-v":
		fmt.Printf("dagrun %s\n", Version)
		return
	case "help", "--help", "-h":
		usage(os.Stdout)
		return
	default:
		fmt.Fprintf(os.Stderr, "dagrun: unknown command %q\n\n", cmd)
		usage(os.Stderr)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "dagrun %s: %v\n", cmd, err)
		os.Exit(1)
	}
}

func usage(w *os.File) {
	fmt.Fprint(w, `dagrun - a work-DAG executor for coding agents

  A node is done only when the artifacts it declared verify.

USAGE
  dagrun plan [FILE] [flags]     validate a plan and install it as the run
  dagrun run [flags]             execute ready nodes by spawning agent harnesses
  dagrun next [flags]            list ready nodes with the prompt to hand an agent
  dagrun claim NODE [flags]      take a node (agent-driven mode)
  dagrun done NODE [flags]       verify then mark a node done
  dagrun fail NODE [flags]       mark failed
  dagrun skip NODE [flags]       mark skipped
  dagrun status [flags]          show the tree and progress
  dagrun validate [flags]        re-check every node against the filesystem
  dagrun trace [flags]           merged execution trace
  dagrun export [flags]          emit agileplus | tracera | json
  dagrun detect [flags]          show detected agent harnesses
  dagrun viz [flags]             render the run as an interactive HTML file
  dagrun draft [flags]           turn an intent paragraph into a valid plan
  dagrun explain NODE [flags]    say why a node is not done and what to do
  dagrun doctor [flags]          read-only health check of the run directory
  dagrun gc [flags]              prune old logs, traces, and merged worktrees
  dagrun workspace [flags]       validate and install a multi-repo workspace
  dagrun timeline [flags]        render the run timeline
  dagrun stats [flags]           per-kind duration, adoption and retry statistics
  dagrun lint [flags]            run the semantic plan linter
  dagrun critique [flags]        write a CRITIQUE.md critique of a plan
  dagrun review [flags]          write a REVIEW.md from the trace
  dagrun template [flags]        print a plan template
  dagrun mcp                     serve the Model Context Protocol over stdio
  dagrun version

COMMON FLAGS
  --dir DIR      run directory (default `+defaultDir+`)
  --json         machine-readable output on stdout

PLAN FLAGS
  --file FILE    plan source, "-" for stdin (default `+defaultDir+`/run.yaml)
  --reset        discard existing progress
  --work N       worker count used for the wall-clock estimate

RUN FLAGS
  --workers N    concurrent nodes in this process (default: plan max_parallel)
  --once         start at most one node then exit
  --dry-run      report what would run, execute nothing
  --owner ID     identity written into leases (default: host.pid.random)
  --lease D      lease duration (default 2m)
  --exec-template S  fallback command, e.g. "mytool --prompt {prompt}"
  --tools A,B    tool allow-list passed to the harness
  --verbose

EXAMPLES
  dagrun plan plan.yaml --json
  dagrun next --json
  dagrun run --workers 4
  dagrun export --format agileplus > bundle.json
`)
}

// flagSet builds a FlagSet with a shared --dir and --json.
func flagSet(name string, dir *string, jsonOut *bool) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(dir, "dir", defaultDir, "run directory")
	if jsonOut != nil {
		fs.BoolVar(jsonOut, "json", false, "machine-readable JSON on stdout")
	}
	return fs
}

// splitArgs separates flags from positionals without requiring flags first.
func splitArgs(args []string) (flags []string, pos []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			flags = append(flags, a)
			// a flag written as --name value: keep the value with the flag
			if !strings.Contains(a, "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				// only for flags that take a value; UnknownFlags are caught by flag itself
				if takesValue(a) {
					flags = append(flags, args[i+1])
					i++
				}
			}
			continue
		}
		pos = append(pos, a)
	}
	return flags, pos
}

var valueFlags = map[string]bool{
	"-dir": true, "-file": true, "-work": true, "-workers": true, "-owner": true,
	"-lease": true, "-exec-template": true, "-tools": true, "-role": true,
	"-count": true, "-ttl": true, "-epoch": true, "-format": true, "-reason": true,
	"-node": true,
}

func takesValue(a string) bool {
	name := a
	if i := strings.IndexByte(a, '='); i >= 0 {
		return false // value is already attached
	}
	// normalize --flag to -flag so the table has one spelling
	name = "-" + strings.TrimLeft(name, "-")
	return valueFlags[name]
}

// nowUTC is the timestamp format used in generated documents.
func nowUTC() string { return time.Now().UTC().Format(time.RFC3339) }

// openRun loads the installed plan and its store.
func openRun(dir string) (*plan.Plan, *store.Store, []byte, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, nil, nil, err
	}
	planPath := filepath.Join(abs, "run.yaml")
	raw, err := os.ReadFile(planPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil, fmt.Errorf("no plan installed at %s; run `dagrun plan FILE` first", planPath)
		}
		return nil, nil, nil, err
	}
	p, err := plan.Parse(raw)
	if err != nil {
		return nil, nil, nil, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, nil, nil, err
	}
	// Artifact paths resolve against the directory dagrun was invoked from.
	p.Root = cwd
	st, err := store.Open(abs, p, raw, false)
	if err != nil {
		return nil, nil, nil, err
	}
	return p, st, raw, nil
}
