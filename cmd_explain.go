package main

import "fmt"

// cmdExplain diagnoses one node: which predicate failed, why, and what to do.
//
// Owner: agent "explain". This stub exists so the dispatch line in main.go can
// be pre-registered before parallel work starts. Replace the body; do not touch
// main.go, which is already wired.
func cmdExplain(args []string) error {
	return fmt.Errorf("explain: not implemented yet")
}
