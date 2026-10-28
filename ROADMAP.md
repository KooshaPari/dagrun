# dagrun — forward WBS

Reporting rule (operator, 2026-10-28): this WBS is printed at the end of every
working turn. The unit of reporting is the **epic / feature / improvement**.
Ephemeral work — PR merges, typo fixes, single-file edits, one-command chores —
is deliberately **not** a line item; it is a subtask of a subtask and is implied
by whatever feature is marked in progress.

States: `DONE` · `WIP` · `NEXT` · `LATER` · `BLOCKED(by x)`.

## Where we are

v0.1.0 shipped and installed. `plan / next / claim / done / fail / skip / status
/ validate / trace / export / detect` all work. 9/9 node DAG completes, rerun
adopts instead of redoing, cross-process claim is exclusive, verifier fails
closed. The single open validation cell is a live harness spawn.

## Epics

```
1.0  VERIFIED AUTONOMY ......................... LATER   trust an unattended run
2.0  FLEET CONCURRENCY ......................... NEXT    20 agents x 4 workers, one repo
3.0  PLAN AUTHORING ............................ NEXT    intent -> validated DAG
4.0  CONTRACTS THAT CATCH SLOP ................. NEXT    beyond exists-and-nonempty
5.0  TRACE AS A PRODUCT ........................ LATER   Tracera natively, humans fast
6.0  AGILEPLUS INTEROP ......................... LATER   a run becomes a feature
7.0  MULTI-REPO INTENTS ........................ LATER   one DAG across repos
8.0  EXTENSIBILITY SURFACES .................... NEXT    MCP, skills, hooks
9.0  OPERATIONAL HARDENING ..................... LATER   never wedges
10.0 DECISION QUALITY .......................... LATER   shrink wrong-DAG risk
```

## 1.0 VERIFIED AUTONOMY — can an operator walk away?

Gate: an unattended overnight run finishes or fails with a reason the operator
can act on without reading logs.

```
1.1 Real-harness proof across families                         NEXT
    1.1.1 jcode: one RESEARCH node, verified, token usage recorded
    1.1.2 codex: one IMPL node, sandbox=workspace-write, verified
    1.1.3 forge + helioslite: one DOC node via -p; confirm exit-code semantics
    1.1.4 kcode: parity check against jcode (fork drift)
    GATE: 4 families each produce a verified artifact from a real prompt

1.2 Cost and time accounting                                  LATER
    1.2.1 extract usage from harness JSON output into NodeState
    1.2.2 dagrun stats: p50/p95 duration by kind, adoption rate, retry rate
    1.2.3 plan-level max_wallclock; abort and report on breach
    GATE: any run can be budgeted before it starts

1.3 Failure taxonomy                                          LATER
    1.3.1 classify: harness_missing | harness_error | timeout |
          contract_miss | cap_blocked | dep_blocked
    1.3.2 persist class; surface in status and explain
    1.3.3 retry policy per class (contract_miss must not blind-retry)
    GATE: every failure names one class and one action

1.4 Crash-recovery proof                                      LATER
    1.4.1 SIGKILL a worker mid-node; rerun; assert once-only side effects
    1.4.2 kill -9 during a claim; assert no double-claim
    1.4.3 document the recovery contract
    GATE: recovery is tested, not asserted
```

## 2.0 FLEET CONCURRENCY — does it hold at N x M?

Gate: 20 executor processes x 4 workers on one repo for an hour, zero
corruption, zero duplicate node executions.

```
2.1 State scaling                                             NEXT
    2.1.1 benchmark state rewrite at 500 nodes x 50 workers; publish numbers
    2.1.2 if p95 lock wait > 20ms, shard per node behind the same lock
    2.1.3 lock backoff + contention report
    GATE: measured p95 lock wait under the target load

2.2 Working-tree isolation                                    NEXT
    2.2.1 isolation: worktree -> git worktree create/remove per node
    2.2.2 merge-back policy: fast-forward only, or leave branch
    2.2.3 plan-time rejection when two nodes declare overlapping file_scope
    GATE: two IMPL nodes edit in parallel without touching each other

2.3 Admission control                                         LATER
    2.3.1 role -> provider map; cap per provider, not just per role
    2.3.2 queue depth and --wait when saturated
    2.3.3 graceful drain on SIGTERM (finish in flight, release leases)
    GATE: a rate limit degrades throughput, never correctness

2.4 Multi-executor safety                                     NEXT
    2.4.1 N independent dagrun run processes on one .dagrun: add the test
    2.4.2 prove no singleton assumption anywhere
    2.4.3 status --watch live view
    GATE: 5 executors, one run, no duplicate work
```

## 3.0 PLAN AUTHORING — how does the DAG get written?

Gate: an agent goes from one paragraph of intent to a validated plan without
hand-writing YAML.

```
3.1 Draft from intent                                         NEXT
    3.1.1 dagrun draft --intent FILE emits a taxonomy-complete skeleton
    3.1.2 always emit INTENT, SPEC, TEST, GATE; infer the rest
    3.1.3 --harness lets a model propose the decomposition; dagrun validates
    GATE: paragraph in, runnable plan out

3.2 Plan linter                                               NEXT
    3.2.1 actionable messages + --fix for mechanical issues
    3.2.2 semantic checks: SPEC with no TEST, IMPL with no SPEC ancestor,
          DOC with no GATE, unreachable node
    3.2.3 dagrun critique writes CRITIQUE.md as a non-blocking artifact
    GATE: a deliberately wrong plan gets a specific, fixable complaint

3.3 Replanning                                                LATER
    3.3.1 replan: diff old/new, keep done nodes, invalidate descendants
    3.3.2 re-verify when a node's contract changes
    3.3.3 plan-hash lineage recorded
    GATE: mid-run plan change never keeps a stale "done"

3.4 Templates                                                 LATER
    3.4.1 greenfield feature | bugfix | migration | audit | next-usable-cvp
    3.4.2 plan --template with flag-substituted placeholders
    3.4.3 examples stay green via the repo's own gate node
    GATE: 5 templates, each validated by the tool itself
```

## 4.0 CONTRACTS THAT CATCH SLOP — is "done" meaningful?

Gate: pasting one document into nine artifacts does not pass.

```
4.1 Kind-aware predicates                                     NEXT
    4.1.1 SPEC: >=1 requirement, each with a WHEN/THEN scenario
    4.1.2 TEST: every SPEC requirement id referenced by >=1 test
    4.1.3 RESEARCH: >=N distinct URLs + Decision/Rationale/Alternatives
    4.1.4 SOTA: >=2 candidates on >=3 axes + explicit verdict
    4.1.5 DOC: internal links resolve
    GATE: a kind-specific placeholder fails its kind

4.2 Coherence checks                                          LATER
    4.2.1 TEST<->SPEC id coverage across nodes
    4.2.2 IMPL file_scope actually touched (diff-based)
    4.2.3 identical sha256 across nodes -> reject
    GATE: cross-node lies are caught

4.3 Anti-gaming                                               LATER
    4.3.1 placeholder markers (TODO/TBD/lorem) fail unless allowlisted
    4.3.2 minimum distinct-line ratio (catches padding)
    4.3.3 LLM review as annotation only, never the authority
    GATE: repetition-padding cannot satisfy a contract

4.4 Contract ergonomics                                       NEXT
    4.4.1 explain NODE: exact failing predicate + fix hint
    4.4.2 validate --explain per node
    4.4.3 one pass + one fail fixture per kind
    GATE: every failure is self-diagnosing
```

## 5.0 TRACE AS A PRODUCT — can a human or Tracera read it?

```
5.1 Tracera submit                                            LATER
    5.1.1 submit --url POSTs /ingest/agileplus; fail loud on 422
    5.1.2 emit the declaration record + content hash Tracera lacks
    5.1.3 idempotent submit keyed by (run, plan hash, node)
    GATE: a run is queryable in Tracera without hand-editing

5.2 Query surface                                             NEXT
    5.2.1 trace --node/--event/--since/--json
    5.2.2 why NODE: deps + last error + failed predicate
    5.2.3 timeline view per run
    GATE: "why is this stuck" answers in one command

5.3 OTel bridge                                               LATER
    5.3.1 one span per node, dag deps in attributes
    5.3.2 OTLP export behind a flag
    5.3.3 document why the DAG lives in the relational tier
    GATE: existing observability stacks can see runs
```

## 6.0 AGILEPLUS INTEROP

```
6.1 Import path                                               LATER
    6.1.1 agileplus import --bundle FILE (~50 lines, AgilePlus side)
    6.1.2 or ship a dagrun-import-agileplus shim
    6.1.3 field-parity test against agileplus-import structs
    GATE: export output imports unchanged

6.2 Round trip                                                LATER
    6.2.1 plan --from-agileplus FEATURE
    6.2.2 wp sequence <-> dagrun id map persisted
    6.2.3 conflict policy on both-sides-changed
    GATE: import -> edit -> re-export preserves structure
```

## 7.0 MULTI-REPO INTENTS

```
7.1 Sub-plans                                                 LATER
    7.1.1 include: namespaced node ids across plan dirs
    7.1.2 cross-plan dependency validation at load
    7.1.3 per-plan artifact roots
    GATE: repo B's node can wait on repo A's node

7.2 Workspace manifest                                        LATER
    7.2.1 dagrun.workspace.yaml listing repos + plans
    7.2.2 status --workspace rollup
    7.2.3 fan-out with per-repo worker budgets
    GATE: one command reports a multi-repo intent
```

## 8.0 EXTENSIBILITY SURFACES — native use, no clever shell

```
8.1 MCP server                                                NEXT
    8.1.1 dagrun mcp over stdio: plan/next/claim/done/fail/status/validate
    8.1.2 resources: current run, node list; prompt: what is next
    8.1.3 local only, no auth, documented
    GATE: an MCP client drives a run without a shell

8.2 Skill / slash wrappers                                    LATER
    8.2.1 /dagrun skill for jcode | kcode | codex | forge
    8.2.2 thin: shell out to the CLI, one code path
    GATE: the same behaviour through every surface

8.3 Hooks                                                     NEXT
    8.3.1 pre-node, post-verify, on-fail shell hooks in the plan
    8.3.2 built-in git-commit ledger hook per verified node
    8.3.3 a failing hook never masks a verification result
    GATE: verified work can auto-commit without weakening verification
```

## 9.0 OPERATIONAL HARDENING — never wedges

```
9.1 Housekeeping                                              LATER
    9.1.1 gc: prune logs/trace by age, compact state
    9.1.2 recover --force: deliberate lease clear
    9.1.3 state schema version + migration
    GATE: a year-old run dir is still usable

9.2 Diagnostics                                               LATER
    9.2.1 doctor: lock contention, expired leases, orphans, disk
    9.2.2 orphan child detection (pid recorded per claim)
    9.2.3 stats (shared with 1.2)
    GATE: one command says whether the run dir is healthy

9.3 Robustness                                                LATER
    9.3.1 fuzz the plan parser
    9.3.2 fuzz glob and must_match handling
    9.3.3 disk-full and read-only-fs behaviour
    GATE: no parser input panics or hangs
```

## 10.0 DECISION QUALITY — shrink the wrong-DAG risk

The known weakness versus Tracera: this tool trusts the first-pass DAG. These
features make that trust cheaper to earn.

```
10.1 Estimate calibration                                     LATER
    10.1.1 record actual vs estimate per node
    10.1.2 drift report by kind; suggest corrected defaults
    GATE: estimates converge after 3 runs

10.2 CVP scoring                                              NEXT
    10.2.1 cvp_gate: true marks the node that closes the checkpoint
    10.2.2 next --by-cvp orders ready work by marginal progress to that gate
    10.2.3 "what is blocking the CVP" chain output
    GATE: "what next" answers in terms of the checkpoint, not the tree

10.3 Review loop                                              LATER
    10.3.1 auto-append a REVIEW node that reads the trace, writes REVIEW.md
    10.3.2 verdict feeds a follow-up plan; never auto-applied
    GATE: every run ends with an explicit judgement
```

## Dependency order

```
1.1 ──► 1.2 ──► 1.3
 │
 └────► 2.2 ──► 2.1 ──► 2.4 ──► 2.3
                 │
8.1 ─────────────┘
 │
 └────► 3.1 ──► 3.2 ──► 3.3 ──► 3.4
                 │
4.4 ◄────────────┘       4.1 ──► 4.2 ──► 4.3
 │
 └────► 5.2 ──► 5.1 ──► 5.3

6.1 ──► 6.2        7.1 ──► 7.2        9.1 ──► 9.2 ──► 9.3

10.2 depends on nothing; ship it early because it changes what "next" means.
```

## Next three

| # | item | why now |
|---|---|---|
| 1 | **1.1 real-harness proof** | the only `NOT_RUN` validation cell; four adapters are written but unproven against a live model. Until this passes, "harness-agnostic" is a claim, not a fact. |
| 2 | **2.2 worktree isolation** | the actual N×M failure mode. Two IMPL nodes on one checkout collide; `workdir` alone does not prevent it. This is the difference between "safe in theory" and "safe with 4 workers". |
| 3 | **8.1 MCP server** | removes the last reason for an agent to hand-roll dispatch. `next`/`claim`/`done` as tools is smaller than any wrapper and makes dagrun the default path instead of the clever one. |

Runner-up: **4.4 + 4.1** (explain + kind-aware predicates). Cheap, and it turns
every failure into a fix instruction, which is what makes an unattended run
recoverable.

## Explicit non-goals

No web UI. No scheduler daemon. No model routing by cost. No intent inference.
No cross-machine distribution of one run (single host, many processes).
