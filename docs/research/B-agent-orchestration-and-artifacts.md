# B — Agent Orchestration Frameworks and Spec-Driven Artifact Models

**Mission.** Design inputs for a Go CLI that generates a subagent-aware PERT/WBS/DAG plan and executes each node with artifact creation, validation, and traceable state.

**Method.** Primary-source reads (official docs + upstream template files) plus read-only inspection of two local repos: `~/CodeProjects/Phenotype/repos/AgilePlus` and `~/CodeProjects/Phenotype/repos/Tracera`. Every framework-specific claim carries a URL in `## Sources`; anything not directly read is marked `UNVERIFIED`. No source files were modified.

---

## Area 1 findings

### 1.1 The six orchestration primitives that actually recur

| Primitive | Systems that implement it | Exact API / mechanism |
|---|---|---|
| **Graph of nodes** | LangGraph | `StateGraph`, `add_node`, `add_edge`, `add_conditional_edges`, `compile()`; `Send` API for map-reduce, `Command` API for state-update-plus-hop |
| **Graph of nodes** | ADK | Graph-based workflows, `SequentialAgent` / `ParallelAgent` / `LoopAgent` class hierarchy, agent routing |
| **Ordered task list, not a graph** | CrewAI | `Task` objects on a `Crew`; `Process.sequential` or `Process.hierarchical`; `context=[...]` wires prior task outputs forward |
| **Direct-edge handoff** | OpenAI Agents SDK | `Agent` + `Runner`, `handoffs=[...]`, no graph object; `Runner.run` with `session=` |
| **Wave-based dependency layering** | Kiro | Kiro parses `tasks.md`, builds a dependency graph, groups independent tasks into **waves**; waves run sequentially, tasks inside a wave concurrently |
| **Feature→milestone decomposition** | Factory Missions | Droid builds a plan of **features** grouped into **milestones**, then Mission Control executes it |

**Finding 1: only LangGraph and ADK are true DAG engines.** CrewAI is a linear list with a `context` back-reference. The OpenAI Agents SDK is direct-edge handoff with no graph primitive at all. Any design that claims "CrewAI gives you a DAG" is wrong.

**Finding 2: Kiro's wave algorithm is the most directly reusable idea for a PERT/WBS generator.** It is a topological levelisation: wave *N* contains nodes whose dependencies are satisfied by wave *N-1*. That is exactly `dag_level` in a PERT critical-path computation, and it gives concurrency for free without a scheduler framework.

### 1.2 Subagent roles and handoffs

| System | Role unit | Handoff mechanism | Return channel |
|---|---|---|---|
| Roo Code | Custom mode (YAML: `slug`, `name`, `roleDefinition`, `groups`, `whenToUse`, `rulesFiles`) | `new_task(mode, message)` from the built-in 🪃 Orchestrator mode | `attempt_completion(result)` — **only the summary returns to the parent** |
| OpenAI Agents SDK | `Agent` | `handoffs=[...]` / explicit handoff tool | `RunResult` handed to next agent |
| CrewAI | `Agent` + `Task` | `Task.context` (list of prior task names); `task.agent` assignment | `TaskOutput` (raw / JSON / Pydantic) |
| ADK | `LlmAgent` + sub-agents | `sub_agents`, routing, A2A protocol | run events / artifacts |
| Cline | Plan mode vs Act mode (not agents) | mode switch, full conversation context carries over | same session |
| Factory Missions | skills reused or developed per feature | orchestration layer (Mission Control) | per-feature validation |

**Finding 3 (most important for the executor): Roo's contract is the one to copy.** The parent passes context *down* explicitly via `message`; the child returns *up* only a completion summary via `attempt_completion(result)`. The docs state this summary "is the source of truth used to keep track of what was completed." This is context isolation by construction: the orchestrator's context stays clean, and the durable record of the child's work is the artifact, not the transcript.

**Finding 4: the child's *artifact* must be first-class, not inferred from chat.** ADK makes this explicit — "**Artifacts**: Enable agents to create and manage persistent outputs like files, code, or documents that exist beyond the conversation lifecycle", with pluggable artifact stores.

### 1.3 State persistence

| System | Store | Key | Format |
|---|---|---|---|
| OpenAI Agents SDK | `SQLiteSession` / `RedisSession` / `SQLAlchemySession` / `MongoDBSession` / `DaprSession` / `EncryptedSession` | session id string | conversation items |
| LangGraph | checkpointer | thread id | graph state snapshot |
| Temporal | service event history | workflow id / activity id | append-only event log |
| DBOS (Go) | Postgres | workflow id (`WithWorkflowID`) | checkpointed step state |
| Restate | Restate runtime | idempotency key | durable handler state |
| OpenAI Agents SDK (HITL) | in-run `RunState` | `result.to_state()` → `state.approve(interruption)` | resumable run state |
| Kiro | `tasks.md` checkboxes | task id | "Progress lives in the tasks.md checkboxes. There is no hidden state." |
| OpenSpec | `tasks.md` checkboxes | task id | same, no hidden state |

**Finding 5: "no hidden state" is a design virtue here.** Both Kiro and OpenSpec deliberately keep progress in a *reviewable file* rather than a database. For an executor that humans audit, the file is the source of truth and the database is a cache.

### 1.4 "Done" / validation gates

| System | Gate mechanism |
|---|---|
| CrewAI | `guardrail` / `guardrails` callables on a `Task`, with `guardrail_max_retries` (default 3); `output_json` / `output_pydantic` for structured output; `human_input` for human review |
| OpenAI Agents SDK | tool `guardrails`; `interruptions` + `to_state()`/`approve()` for human-in-the-loop |
| OpenSpec | `openspec validate` (structural), `openspec status` (artifact completion), `openspec archive` (rejects incomplete changes) |
| spec-kit | **Constitution Check** section inside `plan.md`, re-evaluated after design; "ERROR on gate failures or unresolved clarifications"; `NEEDS CLARIFICATION` markers must be resolved by Phase 0 research |
| spec-kit (bug fix) | final verdict is exactly one of `verified`, `partial`, `failed`; only `verified` confirms a successful fix |
| spec-kit (idea assessment) | decision is exactly one of `go`, `needs-clarification`, `kill` |
| Kiro | requirements.md acceptance criteria; `Correctness` = property-based testing capability; per-task status in the task-execution interface |
| Factory Missions | per-feature validation strategy, plus user-facing QA runs during the mission |

**Finding 6: closed verdict vocabularies are the cheapest reliable gate.** `verified|partial|failed`, `go|needs-clarification|kill`, and CrewAI's retry-bounded guardrail are all enumerable. A gate that can return an unbounded string is not a gate.

### 1.5 Duplicate-work avoidance under concurrency

| Mechanism | System | Detail |
|---|---|---|
| Activity ID space | Temporal | Activity IDs are unique among open executions of a workflow run; standalone activities use a **separate ID space** for deduplication and uniqueness |
| Exactly-once by workflow ID | DBOS (Go) | `dbos.RunWorkflow(ctx, task, i, dbos.WithWorkflowID(exactlyOnceEventID))` |
| Durable queue with dedup | DBOS (Go) | queues support concurrency limits, timeouts, rate limits, deduplication, prioritization |
| Checkpoint resume | DBOS / Temporal | workflows resume from the last completed step after failure; Temporal retries per Activity Retry Policy |
| Worktree isolation | Roo Boomerang / Factory / claude-squad | per-worker isolated checkout so parallel agents cannot collide |
| Wave scheduling | Kiro | independent tasks in one wave run concurrently; the dependency graph prevents duplicate work by construction |
| Plan-then-execute | Cline, OpenSpec, Kiro, spec-kit | the plan is reviewed and frozen before any execution, so the same unit is not re-derived twice |

**Finding 7: dedup must be keyed on the *unit of work*, not on the attempt.** The DBOS `WithWorkflowID` pattern is the model: a stable business key (`exactlyOnceEventID`) identifies the logical unit; retries reuse it, so the underlying store collapses duplicates.

### 1.6 Go-relevant options (Area 1)

| Project | Relevance |
|---|---|
| **DBOS Transact for Go** (`github.com/dbos-inc/dbos-transact-golang`) | The strongest Go-native fit. `dbos.RunAsStep` inside a workflow checkpoints each step to Postgres; `dbos.RegisterQueue` gives durable queues; `WithWorkflowID` gives exactly-once; `dbos.CreateSchedule` + cron; `dbos.Send`/`dbos.Recv` give durable notifications with exactly-once semantics. The README explicitly names "Operating an AI agent, or any application that relies on unreliable or non-deterministic APIs." |
| **Temporal (Go SDK)** | Mature durable execution, Activity ID dedup, event history. Heavier: requires a Temporal service. |
| **Restate (Go SDK)** | Durable building blocks (handlers, virtual objects) with idempotency keys. `UNVERIFIED` for exact Go SDK API surface in this pass. |
| **goose** (`github.com/aaif-goose/goose`, formerly `block/goose`) | Rust CLI/agent, ACP-compatible, MCP extensions. It is an *agent runtime*, not a DAG executor. `UNVERIFIED` for its recipe/task-definition format in this pass. |
| **LangChainGo** (`github.com/tmc/langchaingo`) | Go LangChain port with chains/agents. Provides model + tool primitives, not orchestration state machines. |
| **Tracera Go CLI** (local, `crates/tracera-go-cli`) | Stdlib-only Go CLI already in this ecosystem: `tracera ingest <github\|jira\|agileplus> --file FILE`, `tracera graph <op> --id --depth --direction`, `tracera node <id>`. The new executor should be a sibling of this CLI, sharing the client conventions. |

### 1.7 Anti-patterns to avoid

- **Orchestrator that reads files.** Roo's Orchestrator mode is deliberately limited to `read`-only, and its docs explain why: file reads fill the context, "hampering its ability to remain focused on orchestration." A management-only orchestrator must not do leaf work.
- **Fire-and-forget missions.** Factory's own docs warn Missions "are not fire-and-forget. The orchestrator is an agent, and you can talk to it," and they document failure modes (frozen mission, stuck milestone, direction change, validation failing).
- **Unbounded planning depth.** Factory publishes a heuristic of roughly 1–500 features per Mission and explicitly advises splitting above that.
- **Guessing the parallelisation gain.** Factory states plainly: "Open questions — Is parallelization necessary? Running multiple agents in parallel sounds good in theory but does not actually produce better results than sequential execution? We are testing this." Treat parallel fan-out as a hypothesis, not a default.

---

## Area 2 findings

### 2.1 Artifact families and their on-disk layouts

| Tool | Layout | Artifact kinds |
|---|---|---|
| **spec-kit** | `.specify/memory/constitution.md`; `specs/<NNN-feature>/`; `.specify/bugs/<slug>/`; `.specify/assessments/<slug>/`; `.specify/extensions.yml` | constitution, spec, plan, tasks, research.md, data-model.md, `contracts/`, quickstart.md, checklists, bug report, assessment decision |
| **OpenSpec** | `openspec/config.yaml`; `openspec/changes/<change>/{.openspec.yaml,proposal.md,specs/<cap>/spec.md,design.md,tasks.md}`; `openspec/specs/<cap>/spec.md`; `openspec/changes/archive/<date>-<change>/` | proposal (why), delta specs (what), design (how), tasks (checklist) |
| **Kiro** | `.kiro/steering/{product.md,tech.md,structure.md}` + custom steering; `.kiro/specs/<feature>/` | steering, requirements.md (or bugfix.md), design.md, tasks.md |
| **AgilePlus** (local) | `kitty-specs/<NNN-slug>/{meta.json,spec.md,plan.md,tasks.md}`; `specs/<name>/{meta.json,spec.md,tasks.md,tasks/}`; `agileplus/evidence_ledger.jsonl` | spec, plan, tasks, meta, evidence ledger |
| **Tracera** (local) | `docs/specs/NNN-*.md`; `docs/WP-00-*.md`; `docs/WBS.md` | numbered spec, work package, WBS, traceability matrix |

### 2.2 The canonical four-artifact grammar

OpenSpec states its four artifacts explicitly and gives a drafting order:

```
             ┌─ specs ──┐
proposal ────┤          ├── tasks ── apply
             └─ design ─┘
```

"Proposal comes first. Specs and design follow in either order, and tasks needs both. Implementation (apply) starts once `tasks.md` is in place."

Kiro's three artifacts are the same grammar minus the proposal: `requirements.md` (or `bugfix.md`) → `design.md` → `tasks.md`, in a three-phase workflow.

spec-kit's plan template adds a fourth dimension that the others lack: **research as a first-class artifact with a fixed internal format** — "Decision: [what was chosen] / Rationale: [why chosen] / Alternatives considered: [what else evaluated]" — and it *must* resolve every `NEEDS CLARIFICATION` marker before Phase 1 design can start.

### 2.3 Machine-readable schemas found

**OpenSpec `schema.yaml`** defines artifact order, formats, and templates; `openspec schemas` lists available workflow schemas; `openspec schema fork` creates a custom schema; `openspec templates` resolves template paths per artifact. This is a **schema-driven** artifact system: the artifact set is data, not code.

**OpenSpec delta spec format** (the strictest requirement syntax found anywhere):
```
# Spec Delta
## Purpose
## ADDED Requirements
### Requirement: <name>
<requirement text>
#### Scenario: <name>
- **WHEN** <condition>
- **THEN** <expected outcome>
```

**AgilePlus `agileplus/evidence_ledger.jsonl`** is JSONL — one evidence record per line, append-only, which is the correct shape for a write-once audit trail.

**OpenSpec `.openspec.yaml`** carries per-change metadata including `skip_specs: true`, used to declare "this change has no behaviour delta" (pure refactor/tooling/docs). `openspec validate` *rejects* a zero-delta change without that marker.

### 2.4 Lifecycle states

| Tool | State progression |
|---|---|
| OpenSpec | explore → propose → review → apply → archive; `openspec status` reports artifact completion; archive merges deltas into `openspec/specs/` and moves the folder to `changes/archive/<date>-<change>/`; nothing is deleted |
| Kiro | requirements/bug analysis → design → tasks → parallel wave execution → real-time per-task status |
| spec-kit | constitution (semver) → specify → plan → tasks → implement ⇄ converge until "Converged"; plus separate bug-fix and assessment extensions |
| spec-kit constitution | semver: MAJOR = incompatible principle removal/redefinition; MINOR = new principle or materially expanded guidance; PATCH = clarification/wording/non-semantic. Ratification date is fixed at first adoption; `LAST_AMENDED_DATE` updates on change. Principles must be "declarative, testable, and free of vague language ("should" → replace with MUST/SHOULD rationale)". |
| Cline | Plan mode (read-only, no file edits, no commands) → Act mode (full context retained) → back to Plan on unexpected complexity; checkpoints before Act mode |
| Roo | subtask: created → running → completion summary → parent resumes |
| Factory Missions | plan features+milestones → user approves → Mission Control executes → per-feature validation; mission can be paused/re-steered |

### 2.5 Steering / context-as-a-first-class-artifact

Kiro's steering is a separate artifact class from specs: persistent project knowledge in markdown (`.kiro/steering/`), with **inclusion modes** `always` (default), `fileMatch` (glob-scoped), `manual` (`#steering-file-name`), and `auto` (description-matched). It also supports `#[[file:path]]` live file references, including line ranges and folder listings.

This matters for the executor: steering/context policy and spec/task artifacts are different kinds with different lifecycles, and the inclusion mode is a *node property* (when does this context load?), not a file property.

---

## Proposed artifact taxonomy

### 3.1 Canonical node kinds

| ID | Kind | Definition | Typical source artifacts |
|---|---|---|---|
| `INTENT` | Intent | Why the work exists. The raw ask plus rationale, scope, and acceptance source. | Tracera `AcceptedIntent`; OpenSpec `proposal.md` "Why"; Kiro `requirements.md`; spec-kit bug/assessment intake |
| `SPEC` | Specification | Behavioural contract: requirements + scenarios. No implementation detail. | OpenSpec `specs/<cap>/spec.md`; Kiro `requirements.md`; spec-kit spec |
| `PLAN` | Plan | Sequencing, design decisions, technical context, gates. | OpenSpec `design.md`; Kiro `design.md`; spec-kit `plan.md`; Tracera `WP-00-*` |
| `RESEARCH` | Research | Findings for a specific unknown, with decision/rationale/alternatives. | spec-kit `research.md`; our own reports |
| `SOTA` | State of the art | Prior-art / existing-solution survey compared on named axes. Distinct from RESEARCH: RESEARCH answers "how do we do X"; SOTA answers "does X already exist and who does it". | spec-kit research (research.md best-practices section, partial); no canonical home found — `UNVERIFIED` as a standard artifact across surveyed tools |
| `TEST` | Test | Test plan, test cases, expected results, coverage mapping to requirements. | spec-kit `quickstart.md` + checklists; Kiro `Correctness` |
| `IMPL` | Implementation | Discrete executable task unit. The scheduling unit. | `tasks.md` checkboxes; AgilePlus `work_packages`; Roo subtasks |
| `UI` | User interface | UI/UX artifacts: states, flows, a11y, visual references. | Factory per-feature UI validation; no surveyed tool makes this a distinct file — `UNVERIFIED` |
| `CODE` | Code change | Commits, diffs, PRs, merged changes. | spec-kit implement phase; AgilePlus `pr_url`/`pr_state`; Tracera `Commit`/`PR` node types |
| `EVIDENCE` | Evidence | Proof that a node's gate passed: test output, screenshot, eval score, link. | AgilePlus `evidence` table; `evidence_ledger.jsonl`; spec-kit verdict reports |
| `GATE` | Gate | A named check with pass/fail, evaluated at a node boundary. | spec-kit Constitution Check; CrewAI guardrail; Kiro acceptance criteria |
| `DOC` | Documentation | User-facing or internal docs. | spec-kit `quickstart.md`; Tessl documentation loops |
| `BUG` | Defect | A defect finding and its fix verification. | spec-kit bug extension; AgilePlus `Bug` node type |
| `CONSTITUTION` | Governance | Project-wide principles that constrain all other artifacts. | spec-kit `.specify/memory/constitution.md`; Kiro steering |
| `OTHER` | Other | Anything not matching the above. Must still carry a digest and an owner. | — |

**Why `SOTA` is separated from `RESEARCH`.** The required ID list names both. Every surveyed tool folds them together into one `research.md`, which makes prior-art claims indistinguishable from technical findings. Splitting them lets a reviewer ask "did we re-solve an existing problem?" independently of "did we pick the right technology?" spec-kit's research template already forces the right answer for RESEARCH (Decision / Rationale / Alternatives); SOTA gets the analogous structure (Candidates / Axes / Verdict / Adopt-or-build).

### 3.2 Validation predicates per kind

| Kind | Predicate (all must hold before the node may transition to `DONE`) |
|---|---|
| `INTENT` | non-empty title, rationale, and at least one named acceptance source |
| `SPEC` | ≥1 requirement; each requirement has ≥1 scenario with WHEN and THEN; no implementation nouns (no class/function/library names) |
| `PLAN` | every `NEEDS CLARIFICATION` resolved; every gate named; every dependency resolvable to a node id |
| `RESEARCH` | ≥1 cited URL per claim; decision + rationale + alternatives present |
| `SOTA` | ≥2 candidates compared on ≥3 named axes; explicit adopt-or-build verdict |
| `TEST` | every `SPEC` requirement id covered by ≥1 test case; test command is runnable and recorded |
| `IMPL` | deps satisfied; maps to ≥1 `SPEC` requirement or `INTENT`; file scope declared |
| `UI` | states enumerated; accessibility notes present; visual reference attached |
| `CODE` | artifact digest recorded; build and test pass recorded; commit/PR id linked |
| `EVIDENCE` | non-empty `artifact_path`; `evidence_type`; `metadata` carries the verifier and timestamp |
| `GATE` | status ∈ {pass, fail}; `pass` requires non-null `evidence_ref` |
| `DOC` | links resolve; no orphaned anchors |
| `BUG` | verdict ∈ {verified, partial, failed}; `verified` requires the original symptom re-checked |
| `CONSTITUTION` | version bumped per semver rule; no unexplained bracket tokens; no "should" without MUST/SHOULD rationale |
| `OTHER` | digest + owner + kind justification |

### 3.3 Canonical status vocabulary

`PENDING` → `READY` → `RUNNING` → (`REVIEW` →) `DONE`; plus `BLOCKED`, `FAILED`, `SKIPPED`, `CANCELLED`, `DEFERRED`, `RETIRED`.

This is the union of what the surveyed systems already use, with no invention: AgilePlus `WpState` is `planned|doing|review|done|blocked`; Tracera `IntentStatus` is `accepted|deferred|retired`; spec-kit verdicts are `verified|partial|failed`; OpenSpec transitions are explore/propose/review/apply/archive.

### 3.4 Canonical layout

```
<plan-root>/
  manifest.json           # run id, schema version, node/edge counts, dag_valid
  nodes/<kind>/<id>.md    # artifact body (markdown), one file per node
  nodes/<kind>/<id>.json  # machine-readable node metadata (see §5)
  edges.jsonl             # append-only edge log
  events.jsonl            # append-only execution trace
  evidence/               # gate evidence blobs, content-addressed
```

---

## Conversion mapping

### 4.1 AgilePlus → canonical (field level)

Local schema read from `.agileplus/agileplus.db` (SQLite) and `crates/agileplus-domain/src/`.

| AgilePlus table.column | Canonical node field | Notes |
|---|---|---|
| `work_packages.id` | `node.id` (`IMPL`) | integer id → string, prefix `wp:` |
| `work_packages.title` | `title` | |
| `work_packages.state` | `status` | `planned→PENDING`, `doing→RUNNING`, `review→REVIEW`, `done→DONE`, `blocked→BLOCKED` |
| `work_packages.sequence` | `ordinal` | |
| `work_packages.file_scope` | `file_scope[]` | JSON array |
| `work_packages.acceptance_criteria` | `acceptance` | feeds the `GATE` predicate |
| `work_packages.agent_id` | `assignee` | |
| `work_packages.pr_url` | `external_ref` | |
| `work_packages.pr_state` | `review_state` | `open→OPEN`, `review→REVIEW`, `changes_requested→CHANGES_REQUESTED`, `approved→APPROVED`, `merged→MERGED` |
| `work_packages.worktree_path` | `workdir` | the isolation unit for parallel work |
| `work_packages.feature_id` | `parent` | feature node |
| `wp_dependencies.wp_id → depends_on` | edge `src→dst`, type `blocks` | `dep_type` carries the flavour |
| `intent_nodes.id` | `node.id` | |
| `intent_nodes.node_type` | `kind` | `Intent→INTENT`, `Plan→PLAN`, `Feature→FEATURE`, `Story→STORY`, `Task→IMPL`, `Spec→SPEC`, `Commit→CODE`, `Test→TEST`, `PR→CODE`, `Bug→BUG`, `Artifact→OTHER` |
| `intent_nodes.dag_stage` | `stage` | lowercase mirror of `node_type`, retained for round-trip |
| `intent_nodes.title/description/status/tags/meta/properties` | `title/description/status/tags/meta` | |
| `intent_nodes.table_ref/table_id` | `source_ref` | pointer back to the owning row |
| `intent_edges.source→target` | edge `src→dst` | |
| `intent_edges.relationship_type` | edge `type` | |
| `intent_edges.canonical_map` | `canonical_map` | preserved verbatim for lossless round-trip |
| `trace_links.from_kind/from_id → to_kind/to_id` | edge, type `traces` | the traceability backbone |
| `trace_links.link_type` | edge `type` | |
| `evidence.wp_id` | edge `src=EVIDENCE → dst=IMPL`, type `evidences` | |
| `evidence.fr_id` | edge `src=EVIDENCE → dst=SPEC`, type `satisfies` | requirement coverage |
| `evidence.evidence_type` | `kind` detail | |
| `evidence.artifact_path` | `path` | |
| `gate_results.work_package_id` | edge `src=GATE → dst=IMPL`, type `gates` | |
| `gate_results.gate_name` | `gate` | |
| `gate_results.status` | `status` | `pass→PASS`, `fail→FAIL` |
| `gate_results.evidence_ref` | `evidence_ref` | required non-null when status=pass |
| `gate_results.payload` | `meta` | |
| `intent_graph_metadata.{version,schema_uri,node_count,edge_count,dag_valid,source_system}` | `manifest.*` | the DAG-level header |
| `stories.requirement_id` | edge `src=STORY → dst=SPEC`, type `traces` | |
| `run_records`, `worklog_entries`, `audit_log`, `events` | `events.jsonl` | execution trace |

**Note on `FEATURE` and `STORY`:** AgilePlus's `NodeType` includes `Feature` and `Story`, which have no entry in the required ID list. They are kept as canonical kinds rather than folded into `INTENT`/`SPEC`, because AgilePlus already has a `features` table and `stories` table and collapsing them would destroy traceability. Both are planning-level kinds, not artifact-producing kinds.

### 4.2 Canonical → AgilePlus (write path)

| Canonical | AgilePlus target | Operation |
|---|---|---|
| `INTENT` node | `intent_nodes` row with `node_type='Intent'` | upsert |
| `IMPL` node | `work_packages` row (`state='planned'`, `sequence` = ordinal, `file_scope` = declared scope) | insert |
| `SPEC` requirement | `stories.requirement_id` + `trace_links` row | insert + edge |
| node dependency | `wp_dependencies` row (`dep_type='blocks'`) | insert |
| any edge | `intent_edges` row with `canonical_map` = canonical edge JSON | insert |
| evidence | `evidence` row | insert |
| gate result | `gate_results` row | insert |
| run header | `intent_graph_metadata` row (`dag_valid` from our validator) | upsert |

### 4.3 Tracera → canonical

| Tracera | Canonical | Notes |
|---|---|---|
| `AcceptedIntent` (`crates/tracera-server/src/product/identity.rs`) | `INTENT` node | `kind`: `Requirement`/`Obligation`/`Capability`/`Target` → `intent.kind`; `status`: `accepted→APPROVED`, `deferred→DEFERRED`, `retired→RETIRED`; `baseline: BaselineRevision` → `meta.baseline` |
| `docs/specs/NNN-*.md` | `SPEC` node (numbered, title from filename) | |
| `docs/WP-00-*.md` | `PLAN` node | |
| `docs/WBS.md` | `PLAN` container / `IMPL` list | |
| traceability matrix | `trace_links`-equivalent edges | |
| `crates/tracera-events` records | `events.jsonl` | |

### 4.4 Canonical → Tracera (write path)

Tracera already exposes the ingestion seam: `POST /ingest/github`, `POST /ingest/jira`, `POST /ingest/agileplus`, all accepting `{"issues":[...]}` / `{"items":[...]}` and all returning `BulkIngestionResult{total_processed, requirements_created, trace_links_created, errors}`. `/ingest/agileplus` ingests agents and tasks with an `id` key and the source label `agcord`.

| Canonical | Tracera target | Mechanism |
|---|---|---|
| `INTENT` | `AcceptedIntent` | POST `/ingest/agileplus` with `items[]` |
| `SPEC` requirement | Requirement record | `requirements_created` counter increments |
| canonical edge | `trace_links` | `trace_links_created` counter increments |
| `IMPL` node | agent/task item | `items[]` with `id` key |

**Fail-loud contract to preserve.** The Tracera ingest handlers refuse to fake success: if neither a live source nor a payload is configured, they return HTTP 422 with an explanatory error entry, and `validate_ingest_issues` rejects oversized payloads and > `MAX_INGEST_ISSUES` items. Our CLI should adopt the same contract rather than silently accepting an empty plan.

**Go CLI integration.** `tracera ingest <source> --file FILE` already exists in `crates/tracera-go-cli/main.go` (stdlib-only, `TRACERA_BASE_URL` / `TRACERA_TOKEN` / `TRACERA_TIMEOUT`). The new executor should reuse that client shape and add `tracera dag plan|validate|run|trace` rather than inventing a second HTTP client.

---

## Node metadata for trace-as-DAG

### 5.1 Node

```go
type Kind string
const (
    KindIntent Kind = "INTENT"; KindSpec Kind = "SPEC";       KindPlan  Kind = "PLAN"
    KindResearch Kind = "RESEARCH"; KindSOTA Kind = "SOTA";    KindTest  Kind = "TEST"
    KindImpl Kind = "IMPL";         KindUI   Kind = "UI";       KindCode  Kind = "CODE"
    KindEvidence Kind = "EVIDENCE"; KindGate Kind = "GATE";     KindDoc   Kind = "DOC"
    KindBug Kind = "BUG"; KindConstitution Kind = "CONSTITUTION"; KindOther Kind = "OTHER"
)

type Status string
const (
    StatusPending Status = "PENDING"; StatusReady Status = "READY"; StatusRunning Status = "RUNNING"
    StatusReview Status = "REVIEW"; StatusDone Status = "DONE";     StatusBlocked Status = "BLOCKED"
    StatusFailed Status = "FAILED"; StatusSkipped Status = "SKIPPED"; StatusCancelled Status = "CANCELLED"
    StatusDeferred Status = "DEFERRED"; StatusRetired Status = "RETIRED"
)

type Node struct {
    ID       string            `json:"id"`                    // "<plan>:<kind>:<ordinal>"
    Kind     Kind              `json:"kind"`
    Title    string            `json:"title"`
    Status   Status            `json:"status"`
    Path     string            `json:"path,omitempty"`        // artifact path, repo-relative
    Digest   string            `json:"digest,omitempty"`      // sha256 of artifact content
    Parent   string            `json:"parent,omitempty"`
    Ordinal  int               `json:"ordinal,omitempty"`      // PERT sequence number
    Level    int               `json:"level,omitempty"`       // wave / topological level
    Deps     []string          `json:"deps,omitempty"`
    Assignee string            `json:"assignee,omitempty"`     // agent id or mode slug
    Model    string            `json:"model,omitempty"`
    Gate     string            `json:"gate,omitempty"`         // named gate that must pass
    Accept   string            `json:"acceptance,omitempty"`
    Evidence []string          `json:"evidence,omitempty"`     // evidence node ids
    IdemKey  string            `json:"idem_key"`               // idempotency key
    Attempt  int               `json:"attempt"`
    Started  *time.Time        `json:"started_at,omitempty"`
    Ended    *time.Time        `json:"ended_at,omitempty"`
    Meta     map[string]string `json:"meta,omitempty"`
}
```

### 5.2 Edge

```go
type EdgeType string
const (
    EdgeBlocks EdgeType = "blocks"          // execution ordering / dependency
    EdgeDerives EdgeType = "derives_from"   // artifact provenance
    EdgeTraces EdgeType = "traces"          // requirement / intent traceability
    EdgeEvidences EdgeType = "evidences"    // evidence proves node
    EdgeSatisfies EdgeType = "satisfies"    // evidence satisfies requirement
    EdgeGates EdgeType = "gates"            // gate guards node
    EdgeRefines EdgeType = "refines"        // SPEC refines INTENT
    EdgeHandoff EdgeType = "handoff"        // control passed to next agent
)

type Edge struct {
    ID    string            `json:"id"`
    From  string            `json:"from"`
    To    string            `json:"to"`
    Type  EdgeType          `json:"type"`
    Meta  map[string]string `json:"meta,omitempty"`
}
```

### 5.3 Trace record (one per execution attempt, appended to `events.jsonl`)

```go
type Trace struct {
    RunID     string    `json:"run_id"`
    NodeID    string    `json:"node_id"`
    Attempt   int       `json:"attempt"`
    IdemKey   string    `json:"idem_key"`
    Agent     string    `json:"agent"`
    Model     string    `json:"model,omitempty"`
    Workdir   string    `json:"workdir,omitempty"`   // isolated checkout
    Inputs    []string  `json:"inputs,omitempty"`    // artifact paths consumed
    Outputs   []string  `json:"outputs,omitempty"`   // artifact paths produced
    Digest    string    `json:"digest,omitempty"`
    Gate      string    `json:"gate,omitempty"`
    GatePass  bool      `json:"gate_pass"`
    EvidenceRef string  `json:"evidence_ref,omitempty"`
    Started   time.Time `json:"started_at"`
    Ended     time.Time `json:"ended_at"`
    Summary   string    `json:"summary,omitempty"`    // Roo-style completion summary
    Error     string    `json:"error,omitempty"`
}
```

### 5.4 Idempotency and dedup

```
IdemKey = sha256( run_id | node_id | attempt_group | input_digest )
```

- `input_digest` = hash of the sorted digests of all input artifacts, so a changed input produces a new key and a genuinely new unit of work.
- `attempt_group` is stable across retries of the *same* logical unit, so the underlying store collapses duplicates (the DBOS `WithWorkflowID` pattern).
- `run_id` is stable across a resumed run, so a crash-and-restart does not re-execute completed nodes.

### 5.5 Wave scheduling (Kiro-derived)

1. Compute `Level` = longest path from any root (topological levelisation).
2. Group nodes by `Level`. Level *N* runs only after level *N-1* completes.
3. Within a level, run nodes concurrently, each in its own isolated workdir.
4. A node whose `Deps` are all `DONE` is `READY`; it enters the next wave.

### 5.6 Structural validation (`dag validate`)

Reject the plan if any of the following holds — the same "validate before archive" discipline as OpenSpec, and the same "ERROR on gate failures" rule as spec-kit:

1. unknown dependency id, or dependency on a node in a later level;
2. a cycle (`dag_valid=false` in the manifest, mirroring `intent_graph_metadata.dag_valid`);
3. duplicate node id or duplicate `IdemKey` across concurrently-runnable nodes;
4. a node in `DONE` with no artifact digest;
5. a `GATE` node with status pass and a null `evidence_ref`;
6. a node in `DONE` whose kind's predicate from §3.2 fails;
7. a `TEST` node that covers no `SPEC` requirement;
8. an `IMPL` node with no `Accept` acceptance criteria and no linked `SPEC`/`INTENT`.

### 5.7 Trace-as-DAG invariants

- The trace graph is a **superset** of the plan graph: every plan edge has a corresponding trace edge, and trace edges may add runtime `handoff` and `evidences` edges that were not planned.
- Artifacts are content-addressed; the same digest from two nodes is the same artifact, which makes `derives_from` edges verifiable rather than declarative.
- `events.jsonl` is append-only. The manifest's `node_count` / `edge_count` / `dag_valid` are re-derived and reconciled on every run, and a mismatch is a validation failure, not a warning.

---

## Sources

**Spec-driven artifact models**
- https://github.com/github/spec-kit (README, processes, extensions, presets) — https://raw.githubusercontent.com/github/spec-kit/main/README.md
- https://raw.githubusercontent.com/github/spec-kit/main/templates/commands/plan.md — plan phases, Constitution Check, `research.md` format, `NEEDS CLARIFICATION`, extension hooks
- https://raw.githubusercontent.com/github/spec-kit/main/templates/commands/constitution.md — `.specify/memory/constitution.md`, semver versioning, principle rules, deferred-intent scope guard
- https://openspec.dev/docs/schemas/spec-driven — four artifacts, drafting order, `proposal.md`/`spec.md` templates, delta spec format
- https://openspec.dev/docs/quickstart — change folder layout, review/apply/archive loop, archive merge semantics
- https://openspec.dev/docs/cli — `openspec validate|status|archive|instructions|templates|schemas|schema`, `stores`, `worksets`
- https://openspec.dev/docs/customize — config vs schema customization boundary
- https://kiro.dev/docs/specs/ — `requirements.md`/`design.md`/`tasks.md`, three-phase workflow, wave-based parallel execution
- https://kiro.dev/docs/steering/ — `.kiro/steering/`, `product.md`/`tech.md`/`structure.md`, inclusion modes, `#[[file:...]]`
- https://docs.cline.bot/features/plan-and-act — Plan/Act mode semantics, `/deep-planning`, checkpoints
- https://docs.roocode.com/features/boomerang-tasks — `new_task(mode, message)`, `attempt_completion(result)`, context isolation, Orchestrator read-only limitation
- https://docs.roocode.com/features/custom-modes — mode YAML keys, `.roomodes`, `.roo/rules-{slug}/`
- https://docs.factory.ai/docs/missions/overview — features/milestones, Mission Control, 1–500 heuristic, validation, open parallelisation question
- https://raw.githubusercontent.com/bmad-code-org/BMAD-METHOD/main/README.md — delivery loop, right-sized process, durable context, specialized perspectives, modules (BMad Loop)
- https://google.github.io/adk-docs/agents/ — Artifacts, plugins, callbacks, service registration, graph workflows, A2A
- https://tessl.io/ — registry/skills/loops/factory, evals as gates, "Executable Specs" talk (talk itself not verified)
- https://github.com/tmc/langchaingo — Go LangChain port (chains/agents, no DAG executor)
- https://github.com/aaif-goose/goose — Rust agent runtime, ACP, MCP extensions (recipe/task format `UNVERIFIED`)

**Orchestration frameworks**
- https://docs.langchain.com/oss/python/langgraph/use-graph-api — `StateGraph`, `add_node`/`add_edge`, reducers, `Send`, `Command`
- https://openai.github.io/openai-agents-python/sessions/ — session implementations, `to_state()`/`approve()` interruption resume, `SessionSettings`, `session_input_callback`
- https://docs.crewai.com/en/concepts/tasks — `Task` attributes, `guardrail(s)`, `guardrail_max_retries`, `context`, `output_json`/`output_pydantic`, `Process.sequential`/`hierarchical`, `TaskOutput`
- https://docs.temporal.io/activity-execution — Activity Execution lifecycle, Activity ID uniqueness and separate standalone ID space, retry policy, heartbeat/cancellation
- https://raw.githubusercontent.com/dbos-inc/dbos-transact-go/main/README.md — Go SDK, `RunAsStep`, `RegisterQueue`, `WithWorkflowID` exactly-once, `CreateSchedule`, durable notifications
- https://raw.githubusercontent.com/All-Hands-AI/OpenHands/main/README.md — Agent Canvas, automations, ACP agents, backend isolation
- Restate durable building blocks and idempotency keys — `UNVERIFIED` (docs URL not resolved in this pass)
- AG2 / AutoGen graph-flow API types — `UNVERIFIED` (docs URL not resolved in this pass)

**Local repositories (read-only inspection)**
- `~/CodeProjects/Phenotype/repos/AgilePlus` — `crates/agileplus-domain/src/domain/work_package/mod.rs` (`WpState`, `PrState`, `WorkPackage`), `crates/agileplus-domain/src/intent_graph.rs` (`NodeType`, `DagStage`), `.agileplus/agileplus.db` schema (`work_packages`, `wp_dependencies`, `intent_nodes`, `intent_edges`, `trace_links`, `evidence`, `gate_results`, `intent_graph_metadata`, `stories`), `kitty-specs/`, `specs/`, `agileplus/evidence_ledger.jsonl`
- `~/CodeProjects/Phenotype/repos/Tracera` — `crates/tracera-server/src/product/identity.rs` (`AcceptedIntent`, `IntentKind`, `IntentStatus`, `BaselineRevision`), `crates/tracera-server/src/handlers/ingest_api.rs` (`/ingest/{github,jira,agileplus}`, `BulkIngestionResult`, fail-loud 422), `crates/tracera-go-cli/main.go` (`tracera ingest|query|graph|node`), `docs/specs/`, `docs/WP-00-*`
