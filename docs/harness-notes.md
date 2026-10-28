# Harness notes (observed, 2026-10-27)

Everything here was read from `--help` output and env on the operator's macOS
machine (darwin/arm64). Nothing was inferred from documentation.

## Installed

| name | path | `--version` |
|---|---|---|
| jcode | `/Users/kooshapari/.local/bin/jcode` (= `~/.jcode/builds/current/jcode`) | `jcode v0.99.1 (7206efd0c)` |
| kcode | `~/.local/bin/kcode` → `~/.kcode/builds/current/kcode` (Mach-O arm64) | `kcode v0.93.398-k0.1.0-de…` |
| codex | `~/.local/bin/codex` | `codex-cli 0.165.0` |
| forge | `~/.local/bin/forge` | `forge 2.13.21-h.0.2.7` |
| helioslite | `~/.local/bin/helioslite` | `helioslite 2.13.21-h.0.2.9` |
| helios-cli | not installed | — |

Note: on this machine `PATH` resolves `jcode`/`codex` to `/opt/homebrew/bin`
first (`jcode v0.84.0`, `codex-cli 0.163.0`). `dagrun` uses `exec.LookPath`, so
it reports and runs exactly what a child process would run. Use `dagrun detect`
to see the truth on any given shell.

## Launch detection

A child process can tell which family started it from environment markers.
Observed in a jcode-launched session:

```
JCODE_SESSION_ID, JCODE_SOCKET, JCODE_BIN, JCODE_NON_INTERACTIVE,
JCODE_ACTIVE_PROVIDER, JCODE_RUNTIME_PROVIDER, JCODE_SCRATCH_DIR, ...
FORGE_EDITOR (present, unrelated)
```

`dagrun`'s rules, in order:

1. `KCODE_SESSION_ID` set, or `JCODE_BIN` contains `kcode` → **kcode**
2. `JCODE_SESSION_ID` or `JCODE_SOCKET` set → **jcode**
3. `CODEX_HOME` or `CODEX_SESSION_ID` set → **codex**
4. `HELIOSLITE_SESSION_ID` set → **helioslite**
5. `FORGE_SESSION_ID` or `FORGE_EDITOR` set → **forge**
6. otherwise unknown → fall through to first installed harness

Kcode is a fork of jcode and reads jcode's data through a `home_dir()` shim, so
it may inherit `JCODE_*`. `JCODE_BIN` is the discriminator.

Caveat: `FORGE_EDITOR` is set in unrelated shells (it is a forge plugin
variable, not a session marker), so step 5 is the weakest rule. If a session is
misattributed, pin `harness:` on the node.

## Headless invocation

### jcode / kcode

```
jcode run [OPTIONS] <MESSAGE>

  --json                 machine-readable JSON instead of streamed text
  -p, --provider <P>     jcode | claude | openai | openai-api | openrouter |
                         opencode-go | zai | deepseek | groq | xai | cursor |
                         copilot | gemini | openai-compatible | auto-detect | ...
  -m, --model <MODEL>
  -C, --cwd <DIR>        working directory for the local client process
  --tools <LIST>         explicit allow-list, e.g. bash,read,write,apply_patch
                         ('*' or 'all' for the unrestricted toolset)
  --disabled-tools <L>
  --tool-profile <PROFILE>
  --mcp-tools <MODE>     auto | eager | deferred
  --ndjson
  --resume [<ID>]
  --socket <SOCKET>
```

`dagrun` emits: `jcode run --json -C <dir> -m <model> --tools <list> <prompt>`.

There is no `--timeout` / `--max-turns` flag on `run`; `dagrun` enforces the
timeout by killing the child's process group.

### codex

```
codex exec [OPTIONS] [PROMPT]        # PROMPT or stdin
  -m, --model <MODEL>
  -s, --sandbox <read-only|workspace-write|danger-full-access>
  -C, --cd <DIR>
      --json                          print events as JSONL
  -o, --output-last-message <FILE>
      --skip-git-repo-check
      --ephemeral                     no session files on disk
  -c, --config <key=value>
      --ignore-user-config            (auth still uses CODEX_HOME)
```

`dagrun` emits: `codex exec --json --skip-git-repo-check -s workspace-write
-C <dir> -m <model> <prompt>`.

### forge / helioslite

No `run`/`exec` subcommand for an arbitrary prompt. The global flag is the
headless entry point:

```
forge [OPTIONS] [COMMAND]
  -p, --prompt <PROMPT>     single-shot, then exit; content may be piped
  -C, --directory <DIR>
      --agent <AGENT>
      --conversation <FILE>
      --conversation-id <ID>
      --sandbox <NAME>
```

`dagrun` emits: `forge -C <dir> [--agent <id>] -p <prompt>`.

`forge agent` only has `list`; `forge cmd execute` runs a *predefined* custom
command, not an arbitrary prompt.

### exec fallback

```
--exec-template 'mytool --prompt {prompt} --dir {dir} --model {model}'
```

Quote-aware split; placeholders substituted per whole argv element (so a
multi-line prompt stays one argument). `{stdin}` becomes `-` and the prompt is
written to the child's stdin.

## Concurrency / isolation

Observed risks when many harness processes run under one account:

| risk | mitigation available today |
|---|---|
| shared session directory | `jcode run --json` writes its own session; `codex exec --ephemeral` avoids files, and `CODEX_HOME` can point at a per-worker dir |
| shared provider credentials | all workers share one key; rate limits are per-key, so cap concurrency at the DAG level (`max_parallel`, `role_caps`) |
| shared build cache | harmless (Go/rust caches are content-addressed) |
| shared git working tree | the real hazard: two workers writing one checkout collide. Use `workdir` per node, or `codex --worktree` |

`dagrun` does not isolate working trees. If a DAG has parallel nodes that edit
the same files, give them distinct `workdir` values or serialise them with
`deps`.

## What was not verified

- Whether forge/helioslite `-p` returns a non-zero exit on failure.
- Whether `codex exec --json` flushes its last event before exit.
- Windows behaviour (unsupported by design).
