package main

import "fmt"

// cmdStats prints per-kind duration, adoption and retry statistics.
//
// Owner: the "stats" lane. This stub exists so the dispatch line in main.go can
// be pre-registered before parallel work starts. Replace the body; do not touch
// main.go, which is already wired.
func cmdStats(args []string) error {
	return fmt.Errorf("stats: not implemented yet")
}
