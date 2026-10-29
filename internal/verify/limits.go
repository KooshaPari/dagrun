package verify

import "fmt"

// Hard caps on the artifact predicates an agent can put in a plan. Both
// inputs are attacker-controlled: a plan author (often a model) chooses the
// regex, the glob, and the required substrings. Without caps, a plan can
// hand this package arbitrarily expensive work.
const (
	// MaxMustMatchLength bounds the byte length of Artifact.MustMatch.
	// Longer patterns are rejected without being compiled, so a
	// ReDoS-shaped pattern never reaches the regexp engine.
	MaxMustMatchLength = 4096

	// MaxGlobMatches bounds how many files a single glob may expand to.
	// A pattern like "**" can match the whole tree; we refuse rather than
	// stat and read thousands of files.
	MaxGlobMatches = 5000

	// MaxMustContainLength bounds the byte length of one MustContain
	// entry. Anything longer is a mistake, not a requirement.
	MaxMustContainLength = 1024
)

// checkGlobCap reports an error when a glob expanded past MaxGlobMatches.
// Kept separate from checkArtifact so the cap is testable without building a
// directory with thousands of files.
func checkGlobCap(matches []string) error {
	if len(matches) > MaxGlobMatches {
		return fmt.Errorf("glob matched %d files, limit is %d", len(matches), MaxGlobMatches)
	}
	return nil
}

// checkMustMatchLen reports an error when a regex pattern is over the cap.
func checkMustMatchLen(pattern string) error {
	if len(pattern) > MaxMustMatchLength {
		return fmt.Errorf("pattern is %d bytes, limit is %d bytes", len(pattern), MaxMustMatchLength)
	}
	return nil
}

// checkMustContainLen reports an error when one required substring is over
// the cap.
func checkMustContainLen(want string) error {
	if len(want) > MaxMustContainLength {
		return fmt.Errorf("required text is %d bytes, limit is %d bytes", len(want), MaxMustContainLength)
	}
	return nil
}

// LimitsSummary is the one-line description of the caps, for CLI help and
// error surfaces.
func LimitsSummary() string {
	return fmt.Sprintf("regex <= %d bytes, glob <= %d matches, must_contain <= %d bytes",
		MaxMustMatchLength, MaxGlobMatches, MaxMustContainLength)
}
