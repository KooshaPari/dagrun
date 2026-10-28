# dagrun — design

Status: implemented. `README.md` is the user-facing reference;
`docs/research.md` records what the research changed; `docs/harness-notes.md` is
the observed harness contract. This file is the decision record.

## Problem

Agents need a fast, reliable way to turn an intent into a work DAG, execute it,
and prove each unit happened. `agileplus` and `tracera` will own that eventually.
Today they are not usable as drivers. `dagrun` is the small thing that works now
and exports into both.

## The one invariant

> A node is `done` if and only if every artifact it declared exists and satisfies
> its predicate, and its `check`, if any, exits 0.

Everything else is scheduling. There is no quality judgement, no intent
inference, no model in the control loop. Consequence: an agent cannot claim
success, and a node with no verifiable contract is rejected at plan time.

## Architectural decisions

### D1. On-disk state: JSON + `flock`, not a database

One `state.json` per run, rewritten under an exclusive `flock` with
`tmp` + `fsync` + `rename`.

Rejected `modernc.org/sqlite`: it would be correct (research measured 6 processes
× 25 transactions clean), but v1.61.0 needs Go ≥ 1.26 and the binary grows from
~3 MiB to ~9.6 MiB. Rejected bbolt/Badger: single-process only. Rejected
cgo SQLite: breaks a trivially installable binary.

What is kept from the SQLite design is its *shape*: compare-and-set claim, lease,
epoch fencing, and "my write affected nothing, so I lost ownership". `flock`
provides the same crash property — the kernel drops the lock when the process
dies — so there is no stale-lock reaper anywhere in the codebase.

Bounded by construction: the critical section is microseconds, and it is never
held across a child process.

### D2. Adoption instead of resume flags

If the artifacts verify, the node is done. No separate "resumed" or "already
ran" state to get out of sync. `dagrun run` therefore resumes by simply being
run again, and re-running never duplicates work.

### D3. Trace shards per worker

`.dagrun/trace/<worker>.jsonl`, one JSON object per line, `O_APPEND`. No two
processes write the same file, so there is nothing to contend on. `dagrun trace`
merges by timestamp.

There is no second trace system. The node event *is* the trace.

### D4. Harness as argv, not SDK

`harness.Adapter` is a name, a binary, and an argv builder. `auto` prefers the
harness that launched this process (`JCODE_*` / `KCODE_*` / `CODEX_HOME` /
`FORGE_*` markers), so a worker keeps its family. A node or role may pin another.
`exec` is an escape hatch with a template, and an explicit `--exec-template`
always wins — which is why the whole executor is testable without a model.

### D5. Prompts are generated from the contract

`plan.Prompt` appends the artifact contract and the rules. A node may supply its
own task body, but never the acceptance criteria. This is what makes "done" mean
the same thing in every node.

### D6. WBS ids are hierarchical, estimates are advisory

`1.0` epic, `1.1` task, `1.1.2` subtask, matching the operator convention.
`estimate` feeds PERT, critical path, waves, and the `--workers` wall-clock
estimate. Nothing in verification reads it, so a bad estimate can only produce a
bad printout.

### D7. Export is a pure function of state

`export.AgilePlus` and `export.Tracera` read plan + state + trace and write JSON.
They never mutate. There is no live coupling to either system, so neither can
break a run.

### D8. Windows is refused, not degraded

`flock` does not exist there. Rather than silently assuming one writer per run,
the windows build returns an error. macOS and Linux are the targets.

## Layering

```
plan      schema, structural validation, topo, PERT, prompt      (no I/O except Load)
verify    artifact predicates, check execution                   (reads the filesystem)
store     state document, flock, leases, epochs, caps            (the only writer)
trace     sharded append-only events
harness   detection + argv
run       scheduler, child supervision, outcome
export    agileplus / tracera / json
cmd_*     CLI surface
```

`verify` and `plan` have no back edges; `run` is the only package that combines
them.

## Size

Measured, not estimated. "Code" excludes blank lines and comment-only lines.

| area | raw | code |
|---|---|---|
| CLI (`main.go`, `cmd_*.go`) | 958 | 742 |
| plan (schema, validate, topo, PERT, prompt) | 893 | 628 |
| store (state + leases) | 468 | 331 |
| verify | 291 | 214 |
| run (scheduler + exec) | 480 | 344 |
| harness | 295 | 208 |
| trace | 170 | 108 |
| export | 329 | 219 |
| **production** | **3,884** | **3,366** |
| tests | 868 | 810 |
| **total** | **4,752** | **4,176** |

All files are under 350 lines. The original target was 1–2k with a 5k hard cap;
this landed at 3.4k of code, inside the cap but above the preference.

Where it would shrink if it had to (roughly 700 lines, in order of least loss):

1. `pert.go` wall-clock simulation and per-worker table (~80): keep the critical
   path and waves, drop the makespan heuristic.
2. `cmd_*.go` flag plumbing (~120): using stdlib `flag` unchanged would forbid
   `dagrun plan file.yaml --json`, which is worse than the lines it saves.
3. `export.go` `json` format (~40): `status --json` already exposes state.
4. `harness.go` `exec` template splitter (~50): replace with `sh -c`.
5. Per-kind default artifact contracts (~60): require every node to declare its
   own artifacts.

None of these were cut, because each removes something that earns its lines.

## Validation performed

- `go build ./...`, `go vet ./...` clean.
- `go test ./...` green; `-race` green on the concurrency-critical packages.
- 8 separate OS processes racing one claim → exactly one winner.
- 16 goroutines racing one claim → exactly one winner.
- lease expiry reclaims; stale-epoch `Finish` is rejected.
- global cap and per-role cap both block a second lease inside the lock.
- attempt budget exhausts to `failed`.
- end-to-end: 9-node DAG, 9/9 done via a stand-in agent, `validate` clean.
- idempotency: second run with a deliberately failing template still ends 9/9,
  all adopted, nothing spawned.
- failure path: a placeholder that misses `must_contain` is retried, then failed,
  and the run exits non-zero.

## Open questions

1. Should `dagrun run` auto-commit per node via the `git-commit` ledger wrapper?
2. Should `next --json` also emit TodoWrite-compatible items so a harness's todo
   UI mirrors the DAG?
3. Working-tree isolation for parallel nodes: `workdir` per node works, but a
   git-worktree-per-node mode would be strictly better.
4. Whether to add a `dagrun submit` that POSTs to Tracera's `/ingest/agileplus`
   rather than writing a file.
