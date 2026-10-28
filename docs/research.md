# Research ledger

Seven parallel research lanes fed this design. Raw reports live outside the repo
at `~/Exports/analysis/dag-executor-research/`. This file records what changed
because of them.

| lane | question | outcome |
|---|---|---|
| A | Go DAG/workflow engines to borrow from | **failed twice** (provider stream errors). Substance recovered from lane E plus prior knowledge; see "Landscape" below. |
| B | agent orchestration + spec-driven artifact models | delivered → artifact taxonomy |
| C | harness detection and headless dispatch | **failed twice**; done by hand, see `docs/harness-notes.md` |
| D | Go concurrency and durable state | delivered, with empirical measurements |
| E | prior art, competitors, user complaints | delivered |
| F | AgilePlus artifact model | delivered |
| G | Tracera artifact model | delivered |

## Landscape (lane E)

The gap is real but narrow. Existing projects already claim "artifact as proof
of completion":

- `prd-taskmaster` (Python, MIT): compiles a PRD into a dependency-ordered task
  graph and gates on evidence.
- `codex-factory` (PowerShell): "no artifact = no PASS".
- `pi-dag-core` (TypeScript): evidence observed by the core, not self-reported.
- `lacquer` (Go, Apache-2.0): single-binary YAML AI workflows, dormant since 2025.
- `dagu` (Go, GPL-3.0): one binary, no database; GPL blocks embedding.
- `dagx` (Rust): compile-time cycle prevention, the best executor-core reference.

Nobody is **Go + permissive + active + artifact-gated + dependency-free**. That
is the position claimed here.

Reported failure modes and the minimal mitigation chosen:

| failure | mitigation in dagrun |
|---|---|
| duplicate work re-executed | adoption: a node whose artifacts verify is never re-run |
| runaway loops | parse-time cycle detection + attempt budget + per-node timeout |
| silent node failure | fail closed: `run` exits non-zero, stderr for humans, JSON on stdout |
| lost state on restart | append-only trace + atomic state write; resume is just "run again" |
| stuck task with no completion | explicit `pending`/`running` with `lease_until` and `last_error` |
| framework churn | one dependency, one binary, no migrations |

## Artifact taxonomy (lane B)

Adopted with two changes: `EVIDENCE` was dropped (it is a *result* of
validation, stored per node, not an authored artifact), and `BUG` /
`CONSTITUTION` / `FEATURE` / `STORY` were folded into `OTHER` for the MVP.

Kept as-is: `SOTA` is separate from `RESEARCH`. Every surveyed tool merges them,
which makes "did we re-solve an existing problem?" indistinguishable from "did
we pick the right technology?".

Status vocabulary is the union of the surveyed systems, unnamed:
`pending → running → done`, plus `failed`, `skipped`, `cancelled`. Tracera's
`RunStatus` and AgilePlus's `WpState` both map onto it (see `internal/export`).

Also adopted: the fail-loud ingest contract. `dagrun plan` rejects an empty or
wrong plan rather than silently accepting it, matching Tracera's HTTP 422
behaviour on unconfigured ingest.

## Concurrency (lane D)

The recommendation was `modernc.org/sqlite` in WAL mode, with real measurements:
6 processes × 25 transactions → 150/150 rows, `integrity_check=ok`; a CAS claim
raced across 6 processes → exactly 1 winner; `SIGKILL` mid-transaction → clean
recovery, uncommitted row absent.

**Not followed**, for two concrete reasons the same report surfaced:

1. `modernc.org/sqlite` v1.61.0 requires Go ≥ 1.26.
2. Binary cost is ~9.6 MiB versus ~3 MiB.

Instead the design keeps SQLite's *shape* (atomic compare-and-set claim, lease,
epoch fencing, "0 rows affected means you lost ownership") and gets it from
`flock` + atomic rename, which has the same crash property: the kernel releases
the lock when the process dies, so there is still no stale-lock reaper.

What was taken from lane D verbatim:

- **Epoch fencing.** A slow worker that returns after its lease expired must be
  unable to write. `Finish` requires `owner` and `epoch` to match.
- **Reset only expired leases on startup.** Never blanket-reset `running`, or a
  second live executor gets its work stolen.
- **Cap enforcement must be inside the same critical section as the claim.**
  Counting in a separate transaction is a TOCTOU bug.
- **Never hold the lock across a child wait.** All child supervision happens
  outside.
- **Per-worker trace shards**, not one shared log.
- **Process groups** for children, killed on timeout or lost lease.

The `BEGIN DEFERRED` read-then-write trap that lane D measured (116/150
`SQLITE_BUSY`) does not apply here, but the equivalent rule does: do not read
state, release the lock, and then act on the read.

## AgilePlus harmony (lane F)

`crates/agileplus-import` already defines an anti-lock-in ingest format
(`ImportBundle`) using natural keys — feature `slug`, work-package `sequence`,
`depends_on_sequences` — and deliberately omits every AUTOINCREMENT id.
`dagrun export --format agileplus` emits exactly that shape.

Two rules honoured:

1. Never emit integer ids. `ImportFeature.project_id` is left `null`; the project
   is carried by nesting.
2. Dependencies are intra-feature `sequence` references, never cross-feature.

The importer itself is not wired into the AgilePlus CLI yet (`agileplus-import`
has no binary and no caller). `dagrun` therefore writes a file; wiring
`agileplus import --bundle` is a ~50-line change on the AgilePlus side.

## Tracera harmony (lane G)

Tracera holds four overlapping models and no DAG executor: every
`/api/v1/graph/*` route, including `topo-sort`, is `not_implemented`, and
`queue/status.rs` carries `dag_valid`/`cycles`/`dangles` fields while computing
none of them.

Two findings shaped the export:

1. **A DAG is not a span tree.** `tracera.traces` is strict OTel (one
   `parent_span_id`, 16/8-byte hex) behind a 30-day TTL. A multi-parent node
   cannot live there. The DAG therefore exports into the relational tier:
   `swee_nodes` (`node_type='task'`), `swee_edges` (`depends_on`), `evidence`
   rows for artifacts.
2. **Tracera is missing the two load-bearing pieces** for this tool: an artifact
   *declaration* contract (artifacts are recorded after the fact, never declared
   or hash-bound) and content binding (no hash on `evidence.url`).

`dagrun` supplies both: the plan declares artifacts before execution, and every
validated artifact is exported with `sha256` and byte count.

## What this tool deliberately does less than Tracera

No intent inference, no baseline revisioning, no graph analytics, no ingestion
of GitHub or Jira. It answers one question per node — did the declared artifact
get created and is it real — and leaves interpretation to Tracera.
