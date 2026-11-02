package run

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestChildEnvCarriesOpencodeKeys proves a spawned jcode/kcode gets the
// opencode-go credentials from the env file, not merely whatever the parent
// shell happened to export.
//
// This is a regression test for a real live-run failure: the parent shell
// exported an exhausted pool key, dagrun's child inherited it, and every node
// failed with "GoUsageLimitError: monthly" even though other keys in the pool
// were serving. The provider reads OPENCODE_GO_POOL_KEY (per config.toml), so
// the env file has to be honoured at spawn time.
func TestChildEnvCarriesOpencodeKeys(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	envDir := filepath.Join(home, ".jcode")
	if err := os.MkdirAll(envDir, 0o755); err != nil {
		t.Fatal(err)
	}
	envFile := filepath.Join(envDir, "opencode-go.env")
	body := "OPENCODE_GO_API_KEY=oc_sk_fromfile\nOPENCODE_GO_POOL_KEY=oc_sk_fromfile\n"
	if err := os.WriteFile(envFile, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	// The parent shell exports a DIFFERENT (stale) key. The env file must win.
	t.Setenv("OPENCODE_GO_API_KEY", "oc_sk_stale")
	t.Setenv("OPENCODE_GO_POOL_KEY", "oc_sk_stale")

	env := childEnv()
	seen := map[string]string{}
	for _, kv := range env {
		if i := strings.IndexByte(kv, '='); i > 0 {
			seen[kv[:i]] = kv[i+1:]
		}
	}
	if seen["OPENCODE_GO_API_KEY"] != "oc_sk_fromfile" {
		t.Errorf("OPENCODE_GO_API_KEY = %q, want the env-file key", seen["OPENCODE_GO_API_KEY"])
	}
	if seen["OPENCODE_GO_POOL_KEY"] != "oc_sk_fromfile" {
		t.Errorf("OPENCODE_GO_POOL_KEY = %q, want the env-file key", seen["OPENCODE_GO_POOL_KEY"])
	}
}

// TestChildEnvSurvivesAMissingEnvFile proves a missing env file is not fatal:
// dagrun must still spawn, and the inherited environment is used as-is.
func TestChildEnvSurvivesAMissingEnvFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KEEP_ME", "yes")
	env := childEnv()
	seen := map[string]string{}
	for _, kv := range env {
		if i := strings.IndexByte(kv, '='); i > 0 {
			seen[kv[:i]] = kv[i+1:]
		}
	}
	if seen["KEEP_ME"] != "yes" {
		t.Error("childEnv dropped an unrelated variable")
	}
	if seen["DAGRUN"] != "1" {
		t.Error("childEnv did not mark the child as dagrun-spawned")
	}
}
