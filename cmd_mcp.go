package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"

	"github.com/KooshaPari/dagrun/internal/mcp"
)

// cmdMCP serves the Model Context Protocol over stdio.
//
// stdout is the protocol stream and nothing else: diagnostics go to stderr.
func cmdMCP(args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv := mcp.New(mcp.Config{Version: Version, Log: os.Stderr})
	return srv.Serve(ctx, os.Stdin, os.Stdout)
}
