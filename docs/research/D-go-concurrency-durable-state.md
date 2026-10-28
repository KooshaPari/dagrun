# D — Go: Concurrency + Durable State for a Multi-Process DAG Executor

**Scope.** Single-binary Go CLI coordinating concurrent AI agents. macOS (darwin/arm64) first, Linux deferred.
**Verdict.** SQLite in WAL mode via `modernc.org/sqlite` is the state store. Leases + fencing tokens give crash-safe ownership; process groups give crash-safe supervision. **No third-party scheduler needed.**
**Validation environment.** darwin/arm64, go1.26.5, `modernc.org/sqlite` v1.61.0 (SQLite 3.54.1). All "verified" claims were executed locally, not inferred. Uncertain items are tagged `UNVERIFIED`.

## State store decision

**Decision: `modernc.org/sqlite`, WAL mode, one file per run.**

| Candidate | Multi-process | Verdict |
|---|---|---|
| `modernc.org/sqlite` | Yes (WAL, one writer) | **Chosen.** Pure Go, no cgo, single static binary |
| `mattn/go-sqlite3` | Yes | Rejected. "go-sqlite3 is cgo package… you need gcc"; breaks cross-compile and trivial install |
| `go.etcd.io/bbolt` | **No** | Rejected. Docs: "Bolt uses an exclusive write lock on the database file so it cannot be shared by multiple processes" |
| `dgraph-io/badger/v4` | **No** | Rejected. "Badger uses OS-specific implementations for directory locking" — one process per DB directory |

`modernc.org/sqlite` v1.61.0 ships SQLite **3.54.1** for darwin/arm64 (per its platform table). It is **1.3x–2.0x slower than C SQLite on CPU-bound work** but "throughput across four connections scaled at least as well as the C build" — irrelevant for a control-plane DB holding kilobytes of DAG state.

**WAL-reset bug.** SQLite ≤ 3.51.2 can corrupt a WAL database under two-connection checkpoint/write races; fixed in 3.51.3 (backports 3.44.6, 3.50.7). 3.54.1 is fixed. **Pin the driver version** — do not float it.

**DSN.** Pragmas set by `Exec` on a pooled connection are *not* inherited by other pooled connections (modernc docs: "Connection-scoped state outlives the caller that set it"). Put them in the DSN so every connection gets them:

```go
dsn := "file:" + path +
  "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)" +
  "&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)"
db, _ := sql.Open("sqlite", dsn)
db.SetMaxOpenConns(1)   // one writer per process; see "Concurrency caps"
db.SetMaxIdleConns(1)
db.SetConnMaxLifetime(0)
```

**Durability.** `synchronous=NORMAL` in WAL means the WAL is *not* fsynced on every commit; only checkpoints issue a sync. A **power loss** may roll back recent commits, but a **process crash** cannot (SQLite: with NORMAL, "transactions are no longer durable and might rollback following a power failure or hard reset"). Use `synchronous=FULL` if power-loss durability is required; it costs an fsync per commit.

**Constraints.**
- WAL requires all processes on **one host** (the `-shm` wal-index is shared memory; it cannot live on NFS).
- WAL mode is **persistent**; `page_size` is frozen once in WAL mode.
- **Never `os.Open`/`Close` the DB file in the same process.** SQLite uses POSIX record locks, and the kernel drops every POSIX lock the process holds on a file at *any* `close(2)` of *any* descriptor of that file. modernc v1.58.0+ offers `OFDLocking(true)` to avoid this, but **OFD locks are Linux-only** (`ErrOFDLockingUnavailable` elsewhere), so on macOS this is a hard rule, not a preference.
- Build constraint: `modernc.org/sqlite` v1.61.0 requires **Go ≥ 1.26.0** (verified: go1.23.4 fails with `requires go >= 1.26.0`). The highest driver version supporting Go 1.23.x is `UNVERIFIED` — resolve by reading each candidate's `go.mod` before pinning.
- Binary size cost measured locally: a driver-linked test binary was **9.6 MiB**.

## Lease schema + SQL

**Node state machine.** `pending → ready → running → succeeded | failed | cancelled`, plus `running → ready` (lease expiry requeue) and `running → failed` (attempt budget exhausted).

```sql
PRAGMA journal_mode=WAL;

CREATE TABLE runs (
  id TEXT PRIMARY KEY,
  created_at INTEGER NOT NULL,
  max_parallel INTEGER NOT NULL DEFAULT 4,
  state TEXT NOT NULL DEFAULT 'running'
);

CREATE TABLE nodes (
  id            TEXT PRIMARY KEY,
  run_id        TEXT NOT NULL REFERENCES runs(id),
  state         TEXT NOT NULL CHECK (state IN
                  ('pending','ready','running','succeeded','failed','cancelled')),
  deps          TEXT NOT NULL DEFAULT '',     -- comma-separated node ids
  lane          TEXT NOT NULL DEFAULT 'default',
  payload       TEXT NOT NULL DEFAULT '{}',
  result        TEXT,
  owner         TEXT,                          -- "host:pid:boot-token"
  lease_until   INTEGER NOT NULL DEFAULT 0,    -- unix nanos
  epoch         INTEGER NOT NULL DEFAULT 0,    -- fencing token, bumped on every claim
  attempts      INTEGER NOT NULL DEFAULT 0,
  max_attempts  INTEGER NOT NULL DEFAULT 3,
  not_before    INTEGER NOT NULL DEFAULT 0,    -- backoff gate (unix nanos)
  updated_at    INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX nodes_claimable ON nodes(run_id, state, not_before, lease_until);
CREATE INDEX nodes_lane      ON nodes(run_id, lane, state, lease_until);

CREATE TABLE events (                          -- append-only trace sink
  ts INTEGER NOT NULL, run_id TEXT NOT NULL, node_id TEXT,
  event TEXT NOT NULL, epoch INTEGER, payload TEXT
);
```

**Epoch is the fencing token.** It increments on every successful claim and is echoed by every subsequent write. A worker whose lease expired and was stolen **cannot** mutate the row afterwards, because all its writes carry `AND epoch = ?`.

**Claim (atomic CAS, one statement, one writer).** Select candidates read-only first, then CAS one at a time:

```sql
SELECT id FROM nodes
 WHERE run_id=?
   AND ( (state='ready'   AND not_before<=?)
      OR (state='running' AND lease_until<?) )
 ORDER BY not_before, id
 LIMIT 32;

UPDATE nodes
   SET state='running', owner=?, lease_until=?, epoch=epoch+1,
       attempts=attempts+1, updated_at=?
 WHERE id=?
   AND ( (state='ready'   AND not_before<=?)
      OR (state='running' AND lease_until<?) );
-- RowsAffected()==1 → we own it.  0 → someone else won; try the next candidate.
```

**Heartbeat** (renew; 0 rows ⇒ we lost the lease ⇒ abort the work) and **release** (complete or fail):

```sql
UPDATE nodes SET lease_until=?, updated_at=? WHERE id=? AND owner=? AND epoch=?;

UPDATE nodes
   SET state=?, owner=NULL, lease_until=0, result=?, updated_at=?
 WHERE id=? AND owner=? AND epoch=?;
```

**Transaction discipline — the single most important rule in this report.** SQLite's `BEGIN DEFERRED` starts a *read* transaction if the first statement is a `SELECT`. A later write must **upgrade** it, and the upgrade fails immediately with `SQLITE_BUSY` — the busy handler is not invoked, because invoking it could deadlock. SQLite's docs: "If some other database connection has already modified the database… upgrading to a write transaction is not possible and the write statement will fail with SQLITE_BUSY."

Measured locally, 6 goroutines × 25 read-then-write transactions, `busy_timeout=10000`:

| Pattern | Result |
|---|---|
| `BEGIN DEFERRED` → `SELECT` → `UPDATE` | **116 × `SQLITE_BUSY` / 150 attempts, counter=34** |
| `BEGIN` → `UPDATE` (write first) | **0 errors, counter=150** |

**Rule: every write transaction issues its write statement first**, or uses `BEGIN IMMEDIATE`. In Go: `tx.Exec("BEGIN IMMEDIATE")` on a `*sql.Conn`, or make the first statement a write. `database/sql`'s `db.Begin()` issues plain `BEGIN` (deferred) — do not rely on it for read-modify-write.

## Crash recovery + state machine

**Recovery is SQLite's job, not yours.** On reopen, the first connection replays the WAL and rolls back any incomplete transaction. Verified locally: `SIGKILL` a process holding an open write transaction, then reopen → `PRAGMA integrity_check` = `ok`, the committed row is present, the uncommitted row is **absent**, and the next write succeeded in **0 ms**.

**Locks do not go stale.** SQLite's locks are POSIX advisory locks; the kernel releases them when the owning process dies. No lock-reaper, no PID-file cleanup, no `stale.lock` heuristics. (The same property makes `flock(2)` on a lock file a safe election mechanism on macOS and Linux.)

**Epoch fencing covers the zombie case.** A worker that is merely slow (not dead) may come back after its lease expired and a new owner took over. Its `epoch` is stale, so every conditional write it attempts affects 0 rows. It must treat "0 rows affected" as "I lost ownership → stop the child process → exit".

**Startup recovery procedure (ordered, idempotent, safe with other live executors).**

1. Open the DB; recovery happens automatically. Do **not** reset every `running` node — another executor may be alive and working. Reset only **expired** leases.
2. Requeue expired leases in one IMMEDIATE transaction:
   ```sql
   BEGIN IMMEDIATE;
   UPDATE nodes SET state='ready', owner=NULL, lease_until=0, updated_at=?
    WHERE run_id=? AND state='running' AND lease_until < ?;
   COMMIT;
   ```
3. Recompute readiness: a `pending` node whose dependencies are all `succeeded` becomes `ready`. Do this in the same transaction as the claim, or accept a benign duplicate-claim race that the CAS resolves.
4. Reconcile children: for each `running` node, probe liveness with `syscall.Kill(pid, 0)`. If the process is gone but its lease has not expired, **wait for the lease** rather than stealing it — unless the run is single-owner, in which case steal immediately.
5. Resume scheduling.

**Transition table.**

| From | Event | To | Guard |
|---|---|---|---|
| `pending` | deps satisfied | `ready` | — |
| `ready` | claim | `running` | CAS + `epoch+1` |
| `running` | lease expired | `ready` | `lease_until < now` |
| `running` | lease expired, attempts ≥ max | `failed` | — |
| `running` | child exit 0 | `succeeded` | `owner=? AND epoch=?` |
| `running` | child exit ≠ 0, retries left | `ready` | `not_before = now + backoff` |
| `running` | child exit ≠ 0, no retries | `failed` | — |
| `running` | cancel | `cancelled` | — |
| any | heartbeat | `running` | `owner=? AND epoch=?` |

**Cancellation is two-phase:** set `state='cancelled'`, then signal the process group. The `epoch` guard stops the worker from writing a `succeeded` after cancellation.

## Process supervision

**Goal:** start an agent as a child, know when it dies, and be able to kill its entire subtree.

**Start the child in its own process group.** On darwin, `syscall.SysProcAttr` (verified via `go doc syscall.SysProcAttr`) exposes `Setpgid`, `Pgid`, `Setsid`, `Setctty`, `Ctty`, `Foreground`, `Noctty`, `Chroot`, `Credential`, `Ptrace`.

```go
cmd := exec.Command(bin, args...)
cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: 0}
// Go doc: "Setpgid sets the process group ID of the child to Pgid, or, if Pgid == 0,
// to the new child's process ID." → the child leads a fresh group; pgid == child pid.
cmd.Stdout = logFile      // per-node file, keeps our stdout clean
cmd.Stderr = logFile
cmd.Env = append(os.Environ(), "DAG_RUN_ID="+runID, "DAG_NODE_ID="+nodeID)
if err := cmd.Start(); err != nil { /* release lease */ }
pgid := cmd.Process.Pid    // == pgid because Pgid==0
```

**Signal the group, not the child.** macOS `kill(2)`: "If the process number is negative but not -1, the signal is sent to all processes whose process group ID is equal to the absolute value of the process number."

```go
_ = syscall.Kill(-pgid, syscall.SIGTERM)   // graceful
// wait up to gracePeriod
_ = syscall.Kill(-pgid, syscall.SIGKILL)   // hard
```

Use `cmd.Start()` + `cmd.Wait()` rather than `exec.CommandContext` when you need group semantics: `CommandContext` kills only the direct child, leaking grandchild AI agents. Always `Wait()` — otherwise the child becomes a Unix zombie.

**Supervised-child lifecycle.** `Start` → record `(pid, pgid, startToken)` in the `nodes` row → heartbeat the lease on a ticker → `Wait()` in a goroutine → classify exit → transition. A `Wait()` error of type `*exec.ExitError` means a non-zero exit; `ProcessState.ExitCode()` gives the code, and `ProcessState.Sys().(syscall.WaitStatus).Signaled()` distinguishes "killed by signal" from "exited non-zero" — needed to tell a timeout kill from a genuine agent failure.

**PID reuse hazard.** `syscall.Kill(pid, 0)` proves liveness but cannot detect PID reuse. Store a random `bootToken` in the child's environment and require the child to echo it back (e.g. in its first trace line); compare on reconcile. Reading process start time from libproc on macOS is `UNVERIFIED` here — the env-token approach is portable and needs no extra syscall.

**Signals to the supervisor itself.** `signal.Notify(ch, os.Interrupt, syscall.SIGTERM)` → stop claiming new nodes → `SIGTERM` every live process group → wait `gracePeriod` → `SIGKILL` → release leases → exit non-zero. Do not let the default handler kill the process mid-transaction.

**Linux follow-ups (deferred, `UNVERIFIED` on this host).** `SysProcAttr.Pdeathsig = syscall.SIGKILL` prunes the child if the parent dies; it is Linux-only and absent from the darwin struct. `modernc`'s `OFDLocking(true)` eliminates the stray-`close` POSIX-lock hazard on Linux.

## Concurrency caps

**Enforce the cap in the database, not in memory** — there may be several executor processes on one run, each with its own memory. SQLite serializes writers, so a check-then-act inside one `BEGIN IMMEDIATE` transaction is atomic with respect to other processes. Doing the count in a separate transaction is a TOCTOU bug.

```sql
BEGIN IMMEDIATE;
SELECT COUNT(*) FROM nodes
 WHERE run_id=? AND state='running' AND lease_until > ?;   -- running workers
-- if count >= max_parallel → ROLLBACK, sleep, retry (do not claim)
UPDATE nodes SET ... WHERE id=? AND (...);                   -- the CAS claim
COMMIT;
```

**Per-lane cap** (e.g. one agent per model provider): `SELECT COUNT(*) FROM nodes WHERE run_id=? AND lane=? AND state='running' AND lease_until > ?;`

**Effective limit** = `min(in-process semaphore, global DB cap, lane DB cap)`. The in-process semaphore is a plain buffered channel: it protects the machine (file descriptors, memory) even when the DB cap is generous.

**Pool sizing.** `SetMaxOpenConns(1)` is the safe default. Verified locally: 8 goroutines × 40 writes with `MaxOpenConns=1` → 320/320 rows, 0 errors; with `MaxOpenConns=4` → 320/320 rows, 0 errors *for autocommit single statements*. But the deferred-upgrade deadlock is **not** fixed by a larger pool — it is fixed by write-first ordering. Keep the pool at 1 unless a read-only path needs more, because every extra connection carries its own page cache.

**WAL is one-writer.** "since there is only one WAL file, there can only be one writer at a time." Writes from multiple processes are serialized by SQLite, not by your scheduler. Keep transactions short; never hold a write transaction open across a child-process wait.

## Trace format

**NDJSON, one JSON object per line**, written to stderr and mirrored into the `events` table.

```json
{"ts":"2026-10-28T00:04:11.123Z","run":"r-7f3a","node":"n-research","pid":48213,
 "owner":"host.local:48213:9c1e","event":"node.claim","epoch":4,"attempt":1,
 "dur_ms":0,"attrs":{}}
```

**Required fields:** `ts` (RFC3339 with millis), `run`, `node`, `event`, `owner`. **Include `epoch` on every node event** so a zombie's writes are identifiable after the fact.

**Event vocabulary:** `run.start`, `run.end`, `node.ready`, `node.claim`, `node.heartbeat`, `node.exit`, `node.retry`, `node.succeeded`, `node.failed`, `node.cancelled`, `lease.steal`, `lease.lost`, `cap.blocked`, `db.busy`, `db.locked`.

**`db.busy` must carry the SQLite code.** `SQLITE_BUSY` is 5; `SQLITE_LOCKED` is 6, and 6 | (1<<8) = 262. Log `code` so a busy storm is distinguishable from a lock bug. modernc exposes `sqlite.Error.Code()`.

**Output routing.** Child stdout/stderr → per-node files under `runs/<run>/nodes/<node>.log` (agents are chatty; do not interleave them into the trace stream). Our own stdout → the NDJSON trace only, so the CLI can be piped into `jq`. Writes to the trace must be mutex-guarded and line-buffered, otherwise concurrent goroutines interleave partial lines.

**Heartbeat noise.** Emit heartbeats to the trace at a decimated rate (1-in-10) and keep the DB update at full rate.

## Test plan

Six high-value tests. T1–T5 were executed locally in this session; the numbers are measured results.

| # | Test | Method | Pass criterion | Status |
|---|---|---|---|---|
| T1 | **Multi-process write integrity** | 6 OS processes × 25 write txns on one DB | row count exact (150), `integrity_check=ok`, 0 `SQLITE_BUSY` | **verified: 150/150, 0 errors, ok** |
| T2 | **Exactly one claimer** | 6 OS processes race the CAS claim on one node | sum of `RowsAffected()==1` across processes == 1 | **verified: total_claims=1** |
| T3 | **SIGKILL mid-transaction** | child holds an open write txn; parent `SIGKILL`s it; reopen | `integrity_check=ok`; uncommitted row absent; committed row present; next write succeeds | **verified: ok, absent, present, 0 ms** |
| T4 | **Lease expiry + fencing** | expired lease → reclaimable; live lease → not stealable; stale epoch → write affects 0 rows | reclaim=1, steal=0, stale-write=0 | **verified: reclaim=1, steal=0**; epoch-fencing `UNVERIFIED` (not in harness) |
| T5 | **Deferred-upgrade deadlock guard** | 6 goroutines × 25 read-then-write txns | 0 `SQLITE_BUSY`; counter==150 | **verified failing form: 116 busy, counter=34; write-first form: 0 busy, counter=150** |
| T6 | **Concurrency cap** | cap=2, 6 workers, sample `COUNT(*) WHERE state='running'` | observed peak ≤ 2 | not yet run |

**Additional tests worth having:** (a) process-group kill — child spawns a grandchild, `SIGTERM` the group, assert both die; (b) `go test -race ./...` — the race detector requires cgo and a C compiler on non-Darwin systems, and supports `darwin/arm64`; (c) WAL-reset regression — two connections checkpoint and write at the same instant (SQLite's own reproducers are non-organic; pin the driver version instead of testing this); (d) `-shm` on a network mount → assert the process refuses to start rather than silently corrupting.

**Test harness shape:** `t.TempDir()` per test, `os/exec` re-invoking the test binary itself with a subcommand (the standard `TestHelperProcess` pattern) so children are real OS processes, not goroutines. Real processes are the whole point — the failure modes are cross-process.

## Minimum file/type list

| Type | Responsibility |
|---|---|
| `Store` | Wraps `*sql.DB`; owns DSN, pragmas, pool sizing; all SQL lives here |
| `Node` | Row model: id, state, deps, lane, payload, result, owner, epoch, attempts |
| `NodeState` | String enum + `Valid()` + transition table |
| `Lease` | owner, epoch, until; carried on every claim/heartbeat/release |
| `Claim` | Claim attempt result: node, lease, `Won bool` |
| `Executor` | Scheduler loop: pick ready → check caps → claim → spawn → await → transition |
| `Supervisor` | Process lifecycle: start in group, wait, signal group, reap |
| `Child` | One running child: cmd, pid, pgid, exit classification |
| `Limiter` | Cap evaluation: global, per-lane, per-process |
| `Trace` | NDJSON emitter, mutex-guarded |
| `Event` | Trace event struct with `Epoch` |
| `Config` | DB path, run id, caps, lease duration, grace period, backoff |

**Files (10):**

```
internal/dag/store.go        Store, Node, NodeState; open/close; migrations      ~320 LOC
internal/dag/schema.sql      DDL, embedded via //go:embed                        ~60 LOC
internal/dag/lease.go        Lease, Claim; claim/heartbeat/release; epoch guard  ~180 LOC
internal/dag/limits.go       Limiter; global + lane cap queries                  ~110 LOC
internal/dag/executor.go     Executor; ready-set, scheduler loop, transitions    ~280 LOC
internal/dag/supervisor.go   Supervisor, Child; process-group start/kill/wait    ~190 LOC
internal/dag/recovery.go     startup recovery, requeue, reconcile                ~120 LOC
internal/dag/trace.go        Trace, Event; NDJSON emitter                       ~90 LOC
internal/dag/*_test.go       4 test files (store, lease, executor, supervisor)  ~450 LOC
cmd/<tool>/main.go           CLI wiring: flags, signal handling, trace setup     ~90 LOC
```

**Budget:** ~1,400 LOC production, ~450 LOC tests — under the 1,500-LOC target. Every file is well under the 350-line target; `store.go` is the largest and should be split if it grows.
**Dependency list:** `modernc.org/sqlite` only. No scheduler, no queue library, no lock library.

## Sources

1. SQLite, *Write-Ahead Logging* — <https://www.sqlite.org/wal.html> — one-writer rule, checkpointing, `synchronous=NORMAL` durability caveat, `SQLITE_BUSY` cases, **§11 WAL-Reset bug** (≤3.51.2 affected, fixed 3.51.3), WAL requires same host.
2. SQLite, *Transaction* — <https://www.sqlite.org/lang_transaction.html> — DEFERRED/IMMEDIATE/EXCLUSIVE; "upgrading to a write transaction is not possible and the write statement will fail with SQLITE_BUSY".
3. SQLite, `sqlite3_busy_timeout` — <https://www.sqlite.org/c3ref/busy_timeout.html> — busy-handler semantics.
4. SQLite, *PRAGMA synchronous* — <https://www.sqlite.org/pragma.html#pragma_synchronous>; *Locking* — <https://www.sqlite.org/lockingv3.html>.
5. `modernc.org/sqlite` v1.61.0 — <https://pkg.go.dev/modernc.org/sqlite> — pure Go, SQLite 3.54.1 on darwin/arm64, DSN `_pragma`, connection-scoped state, `OFDLocking` (Linux-only), perf ratios.
6. `go.etcd.io/bbolt` v1.5.0 — <https://pkg.go.dev/go.etcd.io/bbolt> — "exclusive write lock… cannot be shared by multiple processes".
7. `github.com/dgraph-io/badger/v4` — <https://github.com/dgraph-io/badger> — "OS-specific implementations for directory locking".
8. `github.com/mattn/go-sqlite3` — <https://github.com/mattn/go-sqlite3> — "go-sqlite3 is cgo package… you need gcc".
9. Go, `os/exec` — <https://pkg.go.dev/os/exec>; `os/signal` — <https://pkg.go.dev/os/signal>; `syscall.SysProcAttr` — <https://pkg.go.dev/syscall#SysProcAttr> (`Setpgid`/`Pgid`/`Setsid`).
10. Go, *Data Race Detector* — <https://go.dev/doc/articles/race_detector> — requires cgo; supports darwin/arm64.
11. macOS `flock(2)` — <https://developer.apple.com/library/archive/documentation/System/Conceptual/ManPages_iPhoneOS/man2/flock.2.html>; `kill(2)` — <https://developer.apple.com/library/archive/documentation/System/Conceptual/ManPages_iPhoneOS/man2/kill.2.html> (negative pid → process group).

**Local empirical validation (this session, darwin/arm64, go1.26.5, modernc v1.61.0 / SQLite 3.54.1):** T1 6 procs × 25 txns → 150/150 rows, 0 errors, `integrity_check=ok`, WAL file present · T2 CAS claim across 6 OS processes → exactly 1 winner · T3 `SIGKILL` mid-transaction → `integrity_check=ok`, uncommitted row absent, committed row present, post-crash write 0 ms (kernel released the POSIX locks) · T4 expired lease → reclaim `rows=1`; live lease → steal `rows=0` · T5 `BEGIN DEFERRED` read-then-write → 116 `SQLITE_BUSY` / 150, counter=34; write-first → 0 errors, counter=150 · pool sizing `MaxOpenConns` 1 and 4 → 320/320 rows, 0 errors (autocommit statements only) · driver-linked binary size 9.6 MiB.

**`UNVERIFIED` items** (do not treat as facts): highest `modernc.org/sqlite` version supporting Go 1.23.x; epoch-fencing rejection path (T4 third assertion); macOS process-start-time retrieval via libproc; Linux `Pdeathsig` and `OFDLocking` behavior (not exercised on this host); T6 concurrency-cap test (not run).
