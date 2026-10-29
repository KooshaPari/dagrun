# Scale measurements (500-node store, this machine)

Measured, not estimated. Reproduce:

```sh
go test -run 'TestLockWait|TestLockTransition|TestStateSize' -v ./internal/store/
go test -bench 'BenchmarkClaim' -benchtime 200x ./internal/store/
```

## The numbers

| measurement | value |
|---|---|
| state.json at 500 nodes | **90,465 bytes (88.3 KiB)** |
| serialized cost of one claim+finish pair | **25-33 ms** |
| implied last-claimer wait, 10-wide wave | ~250-330 ms |
| implied last-claimer wait, 32-wide wave | ~0.8-1.1 s |
| implied last-claimer wait, 64-wide wave | ~1.6-2.1 s |
| 32 goroutines claiming in a tight loop (p50) | 19-66 ms |
| 32 goroutines claiming in a tight loop (p95) | 1.4-8.1 s |

The spread is the box: this machine was under load from other agents during
the measurements, and a plain 90 KiB file write measured 128 us idle and 737 ms
under load. The numbers above are therefore a range, and the tests assert
**ratios**, not absolute milliseconds.

## What this means

Queue wait is not an independent property: it is `wave size × per-transition
cost`. So the design question is the unit cost, and the unit cost is dominated by
rewriting and renaming the whole 88 KiB document on every transition.

`ROADMAP 2.1.2` proposed a 20 ms p95 lock-wait budget. **That target is not
reachable with a full-document rewrite**, and the measurement above is what
proves it: a 32-wide burst cannot come in under ~800 ms no matter how the lock is
implemented, because the work inside the lock is 25 ms per pair.

## Verdict: sharding is required only above a threshold we do not cross

The normal duty cycle is **not** the burst loop. A worker claims once, then runs
an agent for seconds to minutes. At 80 concurrent workers with 10-minute nodes
that is ~0.13 transitions per second against a sustainable ~40 per second
(1 / 25 ms). The store is nowhere near saturated.

Sharding per node (2.1.2) becomes necessary when **either**:

- more than ~100 claimers can become ready in the same instant (a 100-wide wave
  already costs 2.5 s of queueing), or
- the node count grows well past 500, because the per-transition cost scales with
  document size, or
- heartbeat intervals drop below a second at scale, because heartbeats are the
  highest-frequency transition.

Until then, the honest engineering choice is to keep the single document (it is
what makes `flock` + atomic rename sufficient, with no separate journal) and keep
`max_parallel` modest relative to the wave size.

## Optimisations already applied (measured before/after)

| change | p50 burst | p95 burst | total run |
|---|---|---|---|
| baseline (indent JSON, file fsync + dir fsync) | 1001 ms | 2181 ms | 46 s |
| compact JSON, drop dir fsync, non-durable heartbeat | **19 ms** | 1426 ms | 19 s |

p50 improved by ~50x because the common case no longer waits behind a full
indent-and-two-fsync cycle. p95 barely moved: it is queue depth, not per-op cost,
which is exactly the point above.

Durability trade-off, stated plainly: `state.json` is written with `fsync` then
`rename`, and the directory is **not** fsynced. A power loss can therefore lose
the last transition, never a torn file. Recovery is still sound because a lost
transition means the node runs again and its artifacts are adopted rather than
regenerated. Heartbeats skip `fsync` entirely: losing one only shortens a lease.

## Where the 25 ms actually goes

Measured directly, one step at a time, on a 90 KiB document:

| step | cost |
|---|---|
| write, no fsync | **128 us** |
| write + fsync | **16.0 ms** |
| write + fsync + rename | 9.1 ms (measured separately, fsync-dominated) |

**fsync is the entire cost.** The rename is free. That is why the optimisation
table above works: dropping the directory fsync and the heartbeat fsync removed
two of the three sync points, and p50 fell 50x.

It also means the remaining cost is not reducible without changing the
durability contract, and the contract is deliberate: a power loss may lose the
last transition, never a torn file, and a lost transition means the node runs
again and is adopted rather than regenerated.

## Not yet measured

- per-transition cost at 2,000 nodes (would confirm the linear scaling assumption)
- real fleet behaviour (many OS processes rather than goroutines)
- the same numbers on Linux, where `flock` and APFS behave differently
