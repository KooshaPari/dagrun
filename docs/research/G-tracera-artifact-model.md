# Tracera: artifact model and trace surface (read-only recon)

Scope: `/Users/kooshapari/CodeProjects/Phenotype/repos/Tracera` only. No files edited, no builds, no tests run.
Purpose: decide whether a "Tracera-lite" Go CLI (DAG nodes declare artifacts, artifacts are validated, DAG is the trace) can
supersede Tracera's model or needs a conversion layer.

## What it is

Tracera is a hexagonal trace/observability/audit system for agentic workflows. `README.md` describes it as
"Hexagonal trace-link matrix for agentic + LLM observability — Rust core, React web UI, Electrobun/Tauri desktop."

Crate map (`crates/`):

| Crate | Role |
|---|---|
| `tracera-server` | Axum HTTP server, the Rust core. Hexagonal: `store.rs` trait, `sqlite_store.rs` / `pg_store.rs` adapters |
| `tracera-events` | ClickHouse analytics schema (`src/schema.sql`) + NDJSON ingest CLI (`src/bin/events-ingest.rs`) |
| `tracera-atlas` | Separate crate: work-item delegation engine (`delegation.rs`, `agent_of_record.rs`, `ci_bridge.rs`) |
| `tracera-cli`, `tracera-go-cli/main.go`, `tracera-py-sdk/tracera/client.py` | Three client surfaces (Rust, Go, Python) |
| `tracera-edge` | Cloudflare Worker edge collector (`src/lib.rs`) |
| `tracertm-mcp`, `tracera-mcp` | MCP tool integration |
| `tracera-graphql`, `tracera-ml`, `tracera-neo4j`, `tracera-workos`, `mock-agcord` | GraphQL layer, ML, Neo4j adapter, WorkOS auth, AgCord mock |

Tracera holds **four partially-overlapping models**, not one:

1. **Relational evidence graph** (SQLite/Postgres) — `Evidence`, `TraceLink`, and the SWEE graph.
2. **Analytic event streams** (ClickHouse) — `agent_runs`, `decisions`, `deploys`, `traces`, `llm_calls`.
3. **Product intent model** (`crates/tracera-server/src/product/`) — baseline-revisioned intent items.
4. **Work-item lifecycle** (`crates/tracera-atlas/src/delegation.rs`) — `WorkItemStatus` state machine.

They share vocabulary but not storage, not a foreign key, and not a single query path.

## Trace schema

### A. Relational graph (SQLite/Postgres)

Migrations: `crates/tracera-server/migrations-sqlite/` and `migrations-postgres/`
(`0001_create_evidence.sql`, `0005_create_trace_links.sql`, `0007_swee_graph.sql`, `0010_product_model.sql`).

- `evidence` — `id`, `artifact_id`, `kind`, `url`, `metadata`, `created_at`.
- `trace_links` — `id`, `source_id`, `target_id`, `relationship`, `confidence`, `source`, `created_at`, `updated_at`.
- `swee_nodes` — `id`, `node_type`, `label`, `metadata`, `created_at`, `updated_at`; Rust struct `SwreeNode`
  (`crates/tracera-server/src/swee/mod.rs`).
- `swee_edges` — `id`, `edge_type`, `source_id`, `target_id`; Rust struct `SweeEdge` (same file).
- `product_nodes` / `product_edges` — added by `009_product_model.sql`; `swee_nodes.product_node_id` is a nullable FK
  linking the SWEE layer to the product layer.

`NodeKind` (`crates/tracera-server/src/swee/node_kind.rs`) — 30 variants:
`requirement, specification, design, source_file, module, class, function, test, test_suite, commit, pull_request,
branch, issue, epic, story, task, bug, sprint, release, build, deployment, evidence, problem, incident,
change_request, person, team, environment, artifact, metric`.

`EdgeKind` (`crates/tracera-server/src/swee/edge_kind.rs`) — 32 variants:
`implements, specifies, designs, contains, depends_on, calls, extends, tests, covers, belongs_to, authored_by, touches,
targets, merges_from, fixes, resolves, supersedes, references, blocks, parent_of, in_sprint, owned_by, linked_to,
derived_from, observed_in, triggered_by, correlates_with, impacts, released_in, deployed_to, built_from, emitted_by`.

`NodeKind` and `EdgeKind` are also enforced by SQL `CHECK` constraints, and the module doc states every variant must
appear in `docs/governance/schema/graph_schema.json`.

Trace-link `relationship` values (per `ARCHITECTURE.md`): `implements, verified_by, depends_on, conflicts_with,
satisfies, verifies`.

### B. ClickHouse analytics

`crates/tracera-events/src/schema.sql`, five tables plus one materialized view:

- `tracera.traces` — `trace_id FixedString(32)`, `span_id String`, `parent_span_id Nullable(String)`,
  `name LowCardinality(String)`, `service LowCardinality(String)`, `started_at DateTime64(9)`, `duration_ns UInt64`,
  `status_code LowCardinality(String)` (`OK | ERROR | UNSET`), `attributes String DEFAULT '{}'`.
  MergeTree, `PARTITION BY toYYYYMM(started_at)`, `ORDER BY (service, started_at, trace_id, span_id)`,
  **`TTL toDateTime(started_at) + INTERVAL 30 DAY`**.
- `tracera.agent_runs` — `run_id UUID`, `agent`, `environment`, `status Enum8('Pending','Running','Succeeded','Failed',
  'Cancelled')`, `started_at`, `ended_at Nullable`, `error_message Nullable`, `metadata String`.
- `tracera.decisions`, `tracera.deploys`, `tracera.llm_calls` (`call_id UUID`, `trace_id Nullable(FixedString(32))`,
  `provider`, `model`, `prompt_tokens`, `completion_tokens`, `latency_ms`, `succeeded UInt8`, `called_at`),
  `tracera.llm_calls_daily_mv`.

Rust mirrors in `crates/tracera-events/src/records.rs`: `AgentRun`, `Decision`, `Deploy`, `TraceSpan`, `LlmCall`,
`AgentRunStatus`, `DeployEnvironment`, `Event`.

**The two schemas share no identifier.** The relational graph has no `trace_id`/`span_id`; the ClickHouse `traces`
table has no node/artifact concept.

## Storage

Backend is selected by `DATABASE_URL` in `crates/tracera-server/src/main.rs` (approx. lines 190-230):

- `postgres://` / `postgresql://` → `PgStore` + `sqlx::migrate!("./migrations-postgres")`, labelled "Postgres (server tier)".
- `sqlite://` / `sqlite:` / path ending `.db` → `SqliteStore` + `sqlx::migrate!("./migrations-sqlite")`,
  labelled "SQLite (on-device tier)".
- Anything else → `FATAL: Unrecognised DATABASE_URL scheme.` and process exit.

The `Store` trait (`crates/tracera-server/src/store.rs`) is the only persistence API. It exposes `list_evidence`,
`create_evidence`, `list_sprints`, `create_sprint`, `list_stories`, `create_story`, `create_trace_link`,
`list_trace_links_for_artifact`, `list_teams`, `list_projects`, `count_projects`, `get_project`, `count_evidence`,
`check_readiness`, `list_problems`, `create_problem`, `count_problems`, `count_problems_filtered`,
`dashboard_status_counts`, `create_swee_node`, `create_swee_edge`, `list_swee_nodes`, `list_swee_edges`, `get_swee_node`,
`get_swee_neighbors`. `ListParams` defaults to `page_size = 50`, capped at `MAX_PAGE_SIZE = 500`.

Optional side channels, each `from_env()` and each optional (`crates/tracera-server/src/main.rs`): `cache.rs`
(Redis-style cache with per-key TTL), `neo4j.rs` (`Neo4jClient`), `r2.rs` (`R2Client` object storage with
`presign_get(key, ttl_secs)`), plus `tracera_workos::WorkOSClient`.

ClickHouse is **not** part of `DATABASE_URL`; it is reached separately via `crates/tracera-events/src/clickhouse.rs`.

Retention: the only TTL found is the ClickHouse 30-day `traces` TTL. No purge/prune/retention code was found for
SQLite or Postgres — UNKNOWN for those tiers.

Env vars (`ARCHITECTURE.md` "Configuration"): `DATABASE_URL`, `TRACERA_AUTH_TOKEN`, `TRACERA_BIND_ADDR`,
`TRACERA_PUBLIC_BIND_MODE`, `TRACERA_FRONTEND_DIST`, `TRACERA_RATE_LIMIT_RPS`, `GITHUB_TOKEN`, `GITHUB_REPO`,
`JIRA_URL`, `JIRA_EMAIL`, `JIRA_API_TOKEN`, `JIRA_PROJECT_KEY`. Cargo feature `phenodag-queue` gates the queue module.

## Ingestion API

### Server routes (`crates/tracera-server/src/router.rs`)

**Implemented:** `/healthz`, `/health`, `/readyz`, `/ready`, `/metrics`, `/api/v1/health`, `/api/v1/csrf-token`,
`/api/v1/impact`, `/api/v1/confidence`, `/api/v1/blast-radius`, `/api/v1/governance/spec-check`,
`/api/v1/coverage-matrix`, `/evidence` (GET/POST), `/ingest/github`, `/ingest/jira`, `/ingest/agileplus`,
`/sdlc-pm/sprints`, `/sdlc-pm/stories`, `/api/v1/stories`, `/api/v1/trace` (POST `create_trace_link`),
`/api/v1/dashboard/summary`, `/problems`, `/org-intel/teams`, `/org-intel/metrics`, `/api/v1/auth/me`.

**Stubbed with `any(not_implemented)`:** `/api/v1/items` (+ `/summary`, `/bulk-update`, `/{id}`,
`/{item_id}/pivot`), `/api/v1/links` (+ `/{id}`, `/grouped`, `/projects/{project_id}/links`),
`/api/v1/graph/{ancestors,descendants,impact,dependencies,traverse,path,paths,full,cycles,topo-sort,orphans}`,
`/api/v1/graph/analysis/{centrality,coverage,cycles,dependencies,dependents,impact,metrics}`, `/api/v1/search/*`,
`/api/v1/import`, `/api/v1/auth/{login,logout,refresh,verify}`, `/api/v1/equivalences`.

### Client surfaces

- **Go CLI** (`crates/tracera-go-cli/main.go`): `tracera ingest <github|jira|agileplus>` → POST `/ingest/<source>`;
  `tracera query <infer|suggest|classify|search|confidence>` → POST `/api/v1/<kind>`;
  `tracera graph <ancestors|descendants|impact|dependencies|traverse|full|cycles|orphans|path>` → POST `/api/v1/graph/...`;
  `tracera node <id>` → GET `/api/v1/items/{id}`; `tracera serve` is a reverse proxy.
  Note: `graph` and `node` target only stubbed routes, so those subcommands cannot work today.
- **Python SDK** (`crates/tracera-py-sdk/tracera/client.py`): `ingest_github`, `ingest_jira`, `ingest_agileplus`,
  `query_infer`, `query_suggest`, `query_classify`, `query_search`, `graph(...)`, `get_node`, `list_items`, `healthz`;
  async variant `AsyncTracera`.
- **ClickHouse NDJSON** (`crates/tracera-events/src/bin/events-ingest.rs`): one JSON object per line on stdin, each with
  a `stream` discriminator — `agent_run`, `decision`, `deploy`, `trace`, `llm_call`. Rows are bucketed by stream and
  batch-inserted into the five tables.

### Governance validation

`spec_check` (`crates/tracera-server/src/handlers/governance.rs`, approx. line 296) is the only real validation gate.
Given `specs[]` and `traces[]`, it emits violations for: `duplicate_spec`, `not_approved` (requires
`status == "approved"`), `missing_acceptance` (requires non-empty `acceptance_criteria`), `missing_evidence`
(requires non-empty `evidence_links`), `missing_implementation` (requires a trace of kind `implementation`),
`missing_test` (requires a trace of kind `test`), and `orphan_trace` (trace whose `spec_id` has no spec).
Report shape: `GovernanceReport { status, spec_count, trace_count, violations }` with
`GovernanceViolation { spec_id, code, message }`.

## Intent model

### Product intent (`crates/tracera-server/src/product/`)

- `identity.rs`: `ProductId(pub String)`, `BaselineRevision(pub u64)` (with `ZERO`, `next()`), `IntentKind`
  (`Requirement | Obligation | Capability | Target`), `IntentStatus` (`Accepted | Deferred | Retired`),
  `AcceptedIntent { id, kind, title, description, status, baseline }`.
- `baseline.rs`: `ProductBaseline`, `ProposalStatus`, `BaselineDelta`, `BaselineProposal`.
- `observation.rs`: `Observation`, `ObservationSource`.
- Persistence (`migrations-sqlite/009_product_model.sql`): `product_nodes` (`intent_kind` CHECK in
  `('product','capability','obligation','target')`, `status` CHECK in `('accepted','deferred','retired')`,
  `baseline_revision`), `product_edges` (`edge_type` CHECK in `('product_has_capability',
  'capability_satisfies_obligation','obligation_bound_to_target','observation_covers_capability')`, `weight`).

**Discrepancy (verified):** the Rust `IntentKind` variant is `Requirement` and serializes as PascalCase
(no `rename_all`), while the SQL CHECK allows `product` and the other kinds lowercase. No conversion layer was found
in `store.rs`, `sqlite_store.rs`, or `pg_store.rs`. Also, the `Store` trait has **no** `product_nodes` /
`product_edges` method, so the product tables are DDL-only from the server's perspective.

### Work-item lifecycle (`crates/tracera-atlas/src/delegation.rs`)

`WorkItemStatus` — `Ready`, `InProgress`, `Review`, `Done`, `Blocked`, `Cancelled`; `is_terminal()` returns true for
`Done | Cancelled`; there is a `WorkItemStatus -> SdlcStage` mapping. `agent_of_record.rs`, `ci_bridge.rs`, and
`observability.rs` complete the crate.

### Trace vocabulary

`docs/research/2026-09-04-emergent-garden-evidence-lineage.md` proposes keeping source, observation, assertion,
inference, correction, evaluation, and decision distinct, with `supersedes`, `contradicts`, `qualifies`,
`derived-from`, and `affects-projection` relations, plus `source version/hash` and `candidate artifact hash` fields.
The dossier is explicitly "proposed only" and states that "a global event schema migration is not authorized by this
dossier". None of these fields exist in the current schema.

## Minimum viable compatibility

**SPECULATIVE.** The following mapping is the smallest additive change that lets a Tracera-lite CLI write into
Tracera's existing on-device store without touching the ClickHouse schema:

| Tracera-lite concept | Existing Tracera representation | Change needed |
|---|---|---|
| DAG node | `swee_nodes` row, `node_type = 'task'` | none |
| Node's declared output artifact | `evidence` row (`artifact_id`, `kind`, `url`) | none, if `url` holds a path |
| Node "depends on" node | `swee_edges` row, `edge_type = 'depends_on'` | none |
| Node produced artifact X | `trace_links` row, `relationship = 'implements'`, source = node, target = artifact | none |
| Node validation result | `GovernanceReport` shape, reused with `spec_id` = node id | reuse, not new type |
| Node execution record | `agent_runs` row (`status = 'Succeeded'`) | requires ClickHouse or a local mirror |

Minimum node-type set: `task` (node) and `artifact` (declared output). Minimum edge set: `depends_on`, `implements`.
Minimum relationship set: `implements`, `depends_on`.

The two genuinely missing pieces are a **declaration** record (a node stating the artifacts it will create, before
execution) and a **content binding** (a path plus a hash for each artifact). `evidence.url` is a free-text string with
no hash and no content check; nothing in the repo binds a file to an `evidence` row.

## Gap analysis

1. **No artifact declaration contract.** `Evidence` and `NodeKind::Artifact` record artifacts that already exist.
   Nothing lets a node *declare* what it will create, and nothing validates a declared artifact against the filesystem
   before or after execution. `spec_check` validates spec-to-trace coverage, not artifact-to-file existence.
2. **No DAG executor.** `/api/v1/graph/topo-sort` is `not_implemented`. The only DAG vocabulary in the repo is
   `queue/status.rs` `StatusReport { tasks_total, tasks_by_status, agents_total, agents_stale, dag_valid, cycles,
   dangles }` — and that module contains only a serde round-trip test, computing no cycles and no dangles.
   `grep` for `topo`/`dag` across `crates/tracera-server/src/` finds only these stubs and the queue port.
3. **A DAG is not a span tree.** `tracera.traces` is strict OTel: one `parent_span_id`, hex 16/8-byte ids. A node
   with three dependencies is a DAG, not a tree. Expressing it would require N parent spans (violating OTel semantics)
   or stuffing the dependency set into the free-form `attributes` JSON string, which is not queryable.
4. **Two disconnected stores with different lifetimes.** Graph state lives in SQLite/Postgres; spans live in
   ClickHouse behind a 30-day TTL. Joining a node's evidence to its execution span needs a cross-store join that
   nothing in the repo performs. A node's trace can silently vanish after 30 days while its evidence persists.
5. **The queue is a port, not an implementation.** `crates/tracera-server/src/queue/` ports phenodag v0.3.0 (Go) into
   Rust: `claim.rs`, `lifecycle.rs`, `heartbeat.rs`, `dedup.rs`, `scanner.rs`, `status.rs`, `export.rs`,
   `beads_compat.rs`. `beads_compat.rs` shells out to an external `bd` CLI that must be on `$PATH`. The `tasks`,
   `claims`, and `agents` tables are created by inline `CREATE TABLE` inside function bodies and test helpers, not by
   a migration. `TaskSnapshot { id, status, assigned_agent, updated_at }` and `AgentSnapshot { id, status,
   last_heartbeat }` are the export shapes.
6. **Validation vocabulary mismatch.** `spec_check` wants `status`, `acceptance_criteria`, `evidence_links`, and trace
   kinds `implementation` / `test`. `NodeKind` and `EdgeKind` have no acceptance-criterion or validation concept; the
   closest edges are `covers` (test → requirement) and `tests` (test → source_file). The `governance` trace `kind`
   vocabulary is a separate string space from the SWEE `node_type` / `edge_type` enumerations.
7. **Product layer is unreachable.** `product_nodes` / `product_edges` have DDL but no `Store` trait method and no
   HTTP route; the Rust `IntentKind` string form does not match the SQL CHECK constraint. Reusing the intent model for
   node semantics would require fixing that mapping first.
8. **Trace-link relationships and edge kinds are two taxonomies.** `TraceLink.relationship`
   (`implements, verified_by, depends_on, conflicts_with, satisfies, verifies`) overlaps `EdgeKind`
   (`implements, depends_on, …`) but is not equal to it, and the two are stored in different tables with different
   shapes (`confidence` on one, `edge_type` on the other). Choosing a canonical one is a prerequisite, not a detail.

## Open questions

- UNKNOWN — whether Tracera-lite is meant to write into Tracera's own SQLite store (on-device tier) or to push into
  ClickHouse through `events-ingest`. The two schemas are incompatible and the choice changes the design.
- UNKNOWN — Postgres/SQLite retention. No purge or TTL code was found; only the ClickHouse `traces` 30-day TTL exists.
- UNKNOWN — whether the `/api/v1/graph/*` stubs are planned for implementation. The Go CLI and Python SDK both call
  them today, so both clients currently hit `not_implemented` for graph and node operations.
- UNKNOWN — the intended lifecycle of a DAG node. No `NodeKind` or `EdgeKind` encodes execution state. `queue` has
  `tasks.status` (`ready` / `in_progress`), but that is a different table and a different type family with no FK to
  the SWEE graph.
- UNKNOWN — whether Tracera-lite should be an HTTP client of Tracera (the Go CLI precedent) or embed Tracera's Rust
  types directly. A Go CLI cannot embed them, so the Go precedent implies HTTP.
- UNKNOWN — how artifacts should be identified. `evidence.artifact_id` is a string with no naming rule, no hash, and
  no content binding. Whether the operator wants filesystem paths, repo-relative paths, or content hashes is undecided.
- SPECULATIVE — a `node_artifacts` table (`node_id, artifact_id, kind, path, sha256, created_at, status`) plus a
  `dag_node` view over `swee_nodes` where `node_type = 'task'` would be the smallest additive change. It requires no
  ClickHouse schema change and no change to the 30-day TTL.
- SPECULATIVE — reusing `spec_check`'s rule shape and `GovernanceReport` for artifact validation is cheaper than a new
  validator, because the violation/report types already exist and serialize.
- SPECULATIVE — modeling the DAG as the trace is best done in the relational tier, where `depends_on` edges already
  exist in `EdgeKind` and multi-parent nodes are natural. The ClickHouse `traces` tier should carry execution spans
  only, linked to nodes by a node id carried in `attributes`.
