# dagrun

A work-DAG executor for coding agents. Small, fast, and boring on purpose.

An agent writes a plan. `dagrun` decides what is ready, hands each node to an
agent (or runs it itself), and then **checks the filesystem**: a node is done
only when the artifacts it declared actually exist and satisfy their
predicates. The DAG is the trace, so there is no second system to drift.

```
$ dagrun plan plan.yaml
run: next-usable-cvp   nodes: 9   waves: 6   critical path: 1h

ID       KIND       EST    ES     SLACK  WAVE   TITLE
*1.1     INTENT     10m    0s     0s     0      Freeze the scope of this checkpoint
*1.2     SPEC       10m    10m    0s     1      Write the behavioural contract
 1.3     RESEARCH   10m    10m    40m    1      How do other executors express contracts
...
```

## Why

`agileplus` and `tracera` are the long-term answer. This is the small tool you
can use today: 2k lines of Go, one binary, no daemon, no database, no lock-in.

The one idea it enforces: **"done" means an artifact exists, not that a process
exited 0.** Everything else follows from that.

## Install

```sh
go install github.com/KooshaPari/dagrun@latest   # or:
go build -o ~/.local/bin/dagrun .
```

Requires Go 1.24+. One dependency (`gopkg.in/yaml.v3`, pure Go, vendorable).
macOS and Linux. Windows is refused rather than silently unsafe (no `flock`).

## 60-second demo

```sh
cd /path/to/your/project

# 1. Validate and install a plan. Prints PERT, critical path, waves.
dagrun plan examples/next-cvp/plan.yaml --work 4

# 2. See what is ready, with the exact prompt to hand an agent.
dagrun next --json | jq -r '.ready[].prompt'

# 3a. Let dagrun drive: it spawns a harness per node.
dagrun run --workers 4

# 3b. ...or drive it yourself: claim, do the work, attest.
dagrun claim 1.1 --json
#    ... write artifacts/INTENT.md ...
dagrun done 1.1

# 4. Watch and verify.
dagrun status
dagrun validate
dagrun trace
dagrun export --format agileplus > bundle.json
```

A runnable, model-free demo (a stand-in agent instead of a real one):

```sh
dagrun plan examples/next-cvp/plan.yaml
dagrun run --workers 4 --exec-template 'sh examples/fake-agent.sh {prompt}'
dagrun status      # 9/9
dagrun validate    # 0 broken
```

## The plan

```yaml
version: 1
run: next-usable-cvp
intent: |
  One paragraph: why this run exists. Injected into every node prompt.
artifacts_root: artifacts

max_parallel: 4                 # global cap across all processes
role_caps:                      # optional per-role caps
  research: 2

defaults:
  estimate: 10m                 # advisory, feeds PERT only
  timeout: 20m                  # hard kill for the child process
  max_attempts: 2
  harness: auto                 # auto | jcode | kcode | codex | forge | helioslite | exec

nodes:
  - id: "1.1"                   # hierarchical WBS id: 1.0 epic, 1.1 task, 1.1.2 subtask
    kind: INTENT
    role: intent
    title: Freeze the scope of this checkpoint
    estimate: 10m
    prompt: |                   # optional; dagrun also generates a prompt
      ...
    artifacts:
      - path: artifacts/INTENT.md
        min_bytes: 400
        must_contain: ["Acceptance Criteria"]
        must_match: "^# "       # optional regex
        min_count: 1            # for glob paths
    check: null                 # optional shell gate; exit 0 == pass

  - id: "2.1"
    kind: TEST
    deps: ["1.2"]
    artifacts:
      - path: "internal/store/*_test.go"
        min_count: 1
    check: "go test ./internal/store/"

  - id: "3.2"
    kind: GATE                  # no artifacts; the check IS the artifact
    deps: ["3.1"]
    check: "go build ./... && go test ./..."
```

### Node kinds

| kind | you are declaring |
|---|---|
| `INTENT` | why this work exists, and the acceptance source |
| `SPEC` | a behavioural contract, no implementation |
| `PLAN` | sequencing, design decisions, gates |
| `RESEARCH` | findings for a specific unknown, with sources |
| `SOTA` | prior art compared on named axes, with a verdict |
| `TEST` | tests that cover the spec |
| `IMPL` | implementation, the scheduling unit |
| `UI` | interface states, flows, a11y notes |
| `CODE` | any other code change |
| `DOC` | user-facing or internal documentation |
| `GATE` | a named check at a node boundary |
| `OTHER` | anything else, still with an owner and a digest |

Kinds only decide a **default artifact contract** and the default prompt
wording. They never weaken validation: if a node declares `artifacts` or
`check`, those are what get enforced.

### What "valid" means

All predicates must hold:

1. the path exists (globs allowed, with `min_count`)
2. it is a regular file and is not zero bytes
3. `min_bytes`, if set
4. every string in `must_contain` appears
5. `must_match` regex matches
6. `check`, if set, exits 0 (run with `sh -c` in `workdir`, process group killed on timeout)

A node with no `artifacts` and no `check` is **rejected at plan time** unless
marked `optional: true`. That is the whole safety property: you cannot have a
node that "succeeds" without evidence.

Structural validation (before anything runs) catches: cycles, unknown and
duplicate deps, duplicate ids, unknown kinds, absolute or escaping paths, bad
regexes, unverifiable nodes, `GATE` without `check`.

## CLI

| command | purpose |
|---|---|
| `plan [FILE]` | validate, install as the run, print PERT/WBS |
| `run` | execute ready nodes by spawning a harness |
| `next` | list ready nodes with the prompt to hand an agent |
| `claim NODE` | take a node (agent-driven mode) |
| `done NODE` | verify, then mark done (`--force` overrides) |
| `fail NODE` / `skip NODE` | terminal states with a reason |
| `status` | tree, counts, progress |
| `validate` | re-check every node against the filesystem |
| `trace` | merged execution trace |
| `export` | `agileplus` \| `tracera` \| `json` |
| `detect` | which harnesses are installed, and who launched this process |

Common flags: `--dir` (default `.dagrun`), `--json`.

`run`: `--workers`, `--once`, `--dry-run`, `--owner`, `--lease`,
`--exec-template`, `--tools`, `--verbose`.

## Harnesses

`harness: auto` prefers the harness that launched the current process (detected
from `JCODE_*` / `KCODE_*` / `CODEX_HOME` / `FORGE_*` markers), then the first
installed one. A node or a role can pin a different family, so a DAG can send
research to one and implementation to another.

| harness | headless invocation |
|---|---|
| `jcode` | `jcode run --json -C <dir> -m <model> --tools a,b <prompt>` |
| `kcode` | same shape (fork of jcode) |
| `codex` | `codex exec --json --skip-git-repo-check -s workspace-write -C <dir> -m <model> <prompt>` |
| `forge` | `forge -C <dir> -p <prompt>` |
| `helioslite` | same shape (fork of forgecode) |
| `exec` | your template: `--exec-template 'mytool --prompt {prompt} --dir {dir}'` |

Placeholders for `exec`: `{prompt}`, `{dir}`, `{model}`, `{stdin}`.
An explicit `--exec-template` always wins, which is what makes the tool testable
and lets you wrap any CLI.

`dagrun detect` prints the observed versions on your machine.

## Concurrency

Designed for N agents × M DAG-spawned workers on one repository.

- **State is one JSON document** (`.dagrun/state.json`) rewritten under an
  exclusive `flock`, with `tmp` + `fsync` + `rename`. No daemon, no SQLite, no
  cgo, no dependency.
- **`claim` is atomic across processes.** Dependencies, terminal states, attempt
  budget, global cap, and per-role cap are all checked inside the lock.
- **Leases with epochs.** A crashed worker needs no cleanup: its lease expires
  and the node is requeued. A slow zombie that comes back cannot write a result,
  because its epoch is stale and its write is a no-op.
- **Trace shards are per worker** (`.dagrun/trace/<worker>.jsonl`), so appends
  never contend. `dagrun trace` merges by timestamp.
- **Children run in their own process group**; a timeout or a lost lease kills
  the whole subtree.

Verified in the test suite: 16 goroutines and 8 separate OS processes racing one
claim yield exactly one winner; lease expiry reclaims; stale-epoch writes are
rejected; `-race` is clean.

## Idempotency

Re-running never redoes work. A node whose artifacts still verify is *adopted*
(status `done`, trace event `node.adopt`) without spawning anything. That is
also the crash-recovery path.

Consequence worth knowing: if a sibling already produced a node's artifact, that
node will be adopted rather than run. That is consistent (the artifact is the
proof) but means sibling nodes should never share an artifact path.

## Export / no lock-in

```
dagrun export --format agileplus   # ImportBundle JSON: natural keys only
dagrun export --format tracera     # task nodes, depends_on edges, evidence+sha256
dagrun export --format json        # plan + state + trace
```

`agileplus` emits the shape `crates/agileplus-import` already parses (features
by `slug`, work packages by `sequence`, `depends_on_sequences`, no integer ids).
`tracera` maps onto its relational tier (`swee_nodes` type `task`, `swee_edges`
`depends_on`, one evidence row per artifact with a content hash) because its OTel
span table cannot hold a multi-parent DAG.

The run directory is plain text; nothing here traps your data.

## Non-goals

Not a general workflow engine. No timers, no cron, no retries-with-jitter trees,
no UI server, no consensus. Not a model router. Not an intent inferrer: if the
plan is wrong, the run is wrong, and `dagrun plan` is where you find out.

## Design notes

- **Why JSON + flock and not SQLite.** Every command is short-lived and holds the
  lock for microseconds against a document of kilobytes. SQLite would work and is
  proven at this scale, but it costs a 9.6 MiB binary and a driver that needs Go
  1.26 today. The lock is the election; the file is the truth.
- **Why adoption instead of resume flags.** If the artifact verifies, the node is
  done. There is no separate "resumed" state to get out of sync.
- **Why prompts are generated.** The artifact contract is appended by dagrun, so
  an agent cannot edit away the requirement it is being checked against.

## Layout

```
main.go              CLI dispatch, shared flags
cmd_plan.go          plan: validate, install, PERT
cmd_run.go           run: autonomous execution
cmd_node.go          next/claim/done/fail/skip/validate
cmd_view.go          status/trace/export/detect
internal/plan/       schema, validation, topo, PERT, prompt rendering
internal/store/      flock state, leases, epochs, caps
internal/verify/     artifact predicates and check execution
internal/run/        scheduler and child supervision
internal/harness/    detection and argv for each agent family
internal/trace/      sharded append-only JSONL
internal/export/     agileplus / tracera / json
```

MIT.
