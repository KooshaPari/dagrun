package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/KooshaPari/dagrun/internal/plan"
)

// cmdDraft turns one paragraph of intent into a valid plan.
//
// The emitted plan always contains an intent, a spec, a test, an implementation,
// and a gate, so it validates and can be run without further editing.
func cmdDraft(args []string) error {
	fs := flag.NewFlagSet("draft", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	intentFile := fs.String("intent", "", "file holding the intent text, or - for stdin")
	out := fs.String("out", "", "write the plan here (default stdout)")
	runName := fs.String("run", "", "run name (default: derived from the first line of the intent)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	text, err := readIntent(*intentFile)
	if err != nil {
		return err
	}
	name := *runName
	if name == "" {
		name = deriveRunName(text)
	}

	raw, err := plan.DraftYAML(text, name)
	if err != nil {
		return err
	}
	// Prove the emitted plan is runnable before handing it over.
	p, err := plan.Parse(raw)
	if err != nil {
		return fmt.Errorf("draft produced an unparseable plan: %w", err)
	}
	if issues := p.Validate(); len(issues) > 0 {
		return fmt.Errorf("draft produced an invalid plan: %s", issues[0])
	}

	if *out == "" {
		_, err = os.Stdout.Write(raw)
		return err
	}
	return os.WriteFile(*out, raw, 0o644)
}

func readIntent(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("--intent FILE is required (use - for stdin)")
	}
	if path == "-" {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// deriveRunName makes a readable run name from the first meaningful line.
func deriveRunName(text string) string {
	for _, line := range strings.Split(text, "\n") {
		l := strings.TrimSpace(line)
		if l == "" {
			continue
		}
		l = strings.Trim(l, ".,:;!?\"'")
		if len(l) > 48 {
			l = l[:48]
		}
		return plan.Slug(l)
	}
	return "run"
}
