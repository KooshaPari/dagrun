package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/KooshaPari/dagrun/internal/tracera"
)

// submitValueFlags are the flags that consume the next argument. They are
// normalized to --flag=value before parsing so a flag may appear anywhere on
// the line without the shared splitter needing to know about them.
var submitValueFlags = map[string]bool{
	"--dir": true, "--url": true, "--token": true, "--format": true, "--out": true,
}

// normalizeSubmitArgs rewrites "--flag value" into "--flag=value".
func normalizeSubmitArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			out = append(out, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "-") && !strings.Contains(a, "=") && submitValueFlags[a] && i+1 < len(args) {
			out = append(out, a+"="+args[i+1])
			i++
			continue
		}
		out = append(out, a)
	}
	return out
}

// cmdSubmit posts a run to a Tracera instance. It sends nothing unless the
// payload built, so a plan problem never reaches the network.
func cmdSubmit(args []string) error {
	var dir, url, tokenEnv, format, out string
	var dryRun bool
	fs := flag.NewFlagSet("submit", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&dir, "dir", defaultDir, "run directory")
	fs.StringVar(&url, "url", "http://localhost:3180", "Tracera base URL")
	fs.StringVar(&tokenEnv, "token", "", "name of the env var holding the bearer token")
	fs.StringVar(&format, "format", tracera.FormatTracera, "tracera | agileplus")
	fs.BoolVar(&dryRun, "dry-run", false, "print the JSON body and send nothing")
	fs.StringVar(&out, "out", "", "write the body to a file instead of sending it")
	if err := fs.Parse(normalizeSubmitArgs(args)); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("submit: unexpected argument %q", fs.Arg(0))
	}

	p, st, _, err := openRun(dir)
	if err != nil {
		return err
	}
	snap, err := st.Snapshot()
	if err != nil {
		return err
	}
	payload, err := tracera.BuildPayload(p, snap, format)
	if err != nil {
		return err
	}
	body, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')

	if out != "" {
		if err := os.WriteFile(out, body, 0o644); err != nil {
			return err
		}
		fmt.Printf("wrote %d items to %s\n", len(payload.Items), out)
		return nil
	}
	if dryRun {
		os.Stdout.Write(body)
		return nil
	}

	// The token is named, never typed on the command line, so it stays out of
	// shell history and out of ps output.
	token := ""
	if tokenEnv != "" {
		token = os.Getenv(tokenEnv)
		if token == "" {
			return fmt.Errorf("submit: --token names env var %s, which is empty or unset", tokenEnv)
		}
	}
	c, err := tracera.New(url, token)
	if err != nil {
		return err
	}
	res, err := c.Send(context.Background(), payload)
	if err != nil {
		return err
	}
	fmt.Printf("submitted %d items to %s%s\n", len(payload.Items), c.BaseURL, tracera.IngestPath)
	fmt.Printf("  processed    %d\n  requirements %d\n  trace links  %d\n",
		res.TotalProcessed, res.RequirementsCreated, res.TraceLinksCreated)
	return nil
}
