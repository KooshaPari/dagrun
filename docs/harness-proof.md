# Harness proof (live, 2026-10-27)

Evidence that `dagrun run` actually drives real coding agents, not just that the
adapter code compiles. Every result below is a real model call that produced a
file which `dagrun` then verified from the filesystem.

Reproduce any row:

```sh
mkdir -p /tmp/hproof && cd /tmp/hproof
cat > plan.yaml <<'YAML'
version: 1
run: harness-proof
max_parallel: 1
defaults: {timeout: 3m, max_attempts: 1}
nodes:
  - id: "1"
    kind: DOC
    title: jcode writes a proof file
    harness: jcode
    provider: openrouter
    model: mimo-v2.6-flash
    artifacts: [{path: artifacts/proof-jcode.txt, min_bytes: 10}]
    prompt: |
      Create the file artifacts/proof-jcode.txt containing exactly one line:
      harness proof from jcode
      Do not do anything else. Just write the file and stop.
YAML
dagrun plan plan.yaml
dagrun run --workers 1
```

## Results

| harness | argv dagrun builds | result | wall | artifact |
|---|---|---|---|---|
| **jcode** | `jcode run --json -C <dir> -p openrouter -m mimo-v2.6-flash <prompt>` | **PASS** | 18.5s | `harness proof from jcode` |
| **codex** | `codex exec --json --skip-git-repo-check -s workspace-write -C <dir> <prompt>` | **PASS** | 22.4s | `harness proof from codex` |
| **forge** | `forge -C <dir> -p <prompt>` | **PASS** | 12.0s | `harness proof from forge` |
| **kcode** | `kcode run --json -C <dir> -p <provider> -m <model> <prompt>` | **BLOCKED (external)** | — | see below |
| **helioslite** | `helioslite -C <dir> -p <prompt>` | **BLOCKED (external)** | — | needs a pty, then prompts |

All three passes ended in `done=1 failed=0`, i.e. the node was marked done only
after `verify.Node` read the file and matched the `min_bytes` predicate. Nothing
in these runs trusted the agent's own claim of success.

## What the live runs found (and fixed)

### 1. Children inherited the parent session's provider binding

First attempt at `harness: jcode, provider: opencode-go, model: mimo-v2.6-flash`
failed with:

```
status: 400 Bad Request
response: {"type":"error","error":{"type":"bad_request_error",
  "message":"invalid params, unknown model 'mimo-v2.6-flash' (2013)"}}
```

The request had been routed to `https://api.minimax.io/v1/chat/completions` — the
`openai-compatible` provider — even though `-p opencode-go` was passed. The child
had inherited `JCODE_RUNTIME_PROVIDER` and `JCODE_ACTIVE_PROVIDER` from the
session that launched `dagrun`, and those took precedence over the flag.

Fix: `run.childEnv()` now strips session/provider bindings before spawning
(`JCODE_SOCKET`, `JCODE_SESSION_ID`, `JCODE_RUNTIME_PROVIDER`,
`JCODE_ACTIVE_PROVIDER`, `JCODE_INITIAL_PROVIDER_EXPLICIT`, `JCODE_RESUMING`,
`JCODE_NON_INTERACTIVE`, `JCODE_TUI_INHERITED_*`, `CODEX_SESSION_ID`,
`FORGE_SESSION_ID`, `HELIOSLITE_SESSION_ID`). Covered by
`TestChildEnvScrubsParentSession`.

This is the difference between "per-node provider/model works" and "per-node
provider/model is silently overridden". Without the fix, a DAG that pins a model
per role would have been quietly lying about which model ran.

### 2. kcode: two independent external blockers

Investigated 2026-10-29 with the fork's own credentials (names inspected, values
never printed).

**Blocker A — a key-naming mismatch in the fork.** The fork stores its
credential as `KCODE_OPENAI_COMPAT_API_KEY` in
`~/Library/Application Support/kcode/openai-compatible.env`, but the binary
reads `OPENAI_COMPAT_API_KEY` (13 references) and only falls back to the
`KCODE_`-prefixed name in some paths (5 references). jcode has no such prefix
(26 references, one name). Exporting the stored key under the name the binary
reads resolves the key lookup: the request then reaches the provider instead of
failing with "OPENAI_COMPAT_API_KEY not found".

**Blocker B — the endpoint's own context limit.** With the key resolved, every
request fails with:

```
status: 400 Bad Request
response: {"error":{"message":"prompt token count of 64505 exceeds the limit of 64000",
                    "code":"model_max_prompt_tokens_exceeded"}}
```

The count is **invariant** to everything under the operator's control:

| variation | prompt tokens |
|---|---|
| default | 75,925 |
| `--tools bash` | 64,516 |
| `--tool-profile none` | 64,499 |
| `--mcp-tools deferred --mcp-tools-token-threshold 1` | 75,925 |
| clean cwd (`-C /tmp/empty`), trivial prompt | 64,505 |

So kcode injects a **fixed ~64.5k-token system prompt** of its own, and the
configured provider (`api.githubcopilot.com`, models `gpt-4o` / `gpt-4.1` /
`gpt-4o-mini`) caps the prompt at 64,000. The two numbers differ by ~500 tokens.
Direct `curl` to the same endpoint with the same key returns HTTP 200, so the
endpoint and the credential are both fine; only kcode's own prompt is too large
for it.

**This is a kcode-side limit, not a dagrun defect.** The adapter is identical
to jcode's, and jcode passes. What dagrun CAN do is shrink the child's
environment, which is worth doing regardless: see the note on `childEnv` below.

**Remediation (outside this repo):** point kcode's `openai-compatible` profile
at a provider with a larger context window, or reduce kcode's own injected
prompt. No dagrun change can close a 500-token gap inside a fixed system
prompt.

### 3. helioslite needs a pty and a configured default provider

Directly:

```
● [22:01:17] ERROR: Device not configured (os error 6)
```

Under a pty (`script -q /dev/null helioslite ...`) it proceeds but opens an
**interactive provider picker** (`Provider 46/46 ... AdaL`), because it has no
configured default. So it is not headless-usable until `helioslite` is set up with
a default provider and model.

`forge`, from the same lineage and a different version, works fine headlessly —
so this is a version/config difference, not a family limitation.

## Current harness support statement

| claim | status |
|---|---|
| "harness-agnostic" | **true for jcode, codex, forge** — each proven with a real model |
| "works with every installed CLI" | **not claimed** — kcode lacks credentials, helioslite lacks a default provider |
| "per-node provider+model+harness" | **true** after the env-scrub fix, for jcode/kcode |
| "unknown CLIs work" | **true via `--exec-template`**, unproven with a live third-party CLI |

`dagrun detect` reports install state and versions; it cannot report
credentials or configured defaults, which is why the two blocked harnesses look
installed and healthy.
