# F — AgilePlus artifact model (recon)

Repo: `~/CodeProjects/Phenotype/repos/AgilePlus` @ `9a4f9464` · read-only recon, no builds, no repo edits, CLI `--help` only. Every name/path below is
real and observed unless marked `UNKNOWN` / `SPECULATIVE`.

## What it is

AgilePlus is the Phenotype-org **spec-driven development framework**: a local-first work-tracking platform of ~29 Rust crates (`agileplus-*` +
`traceability-core`) under a hexagonal port/adapter architecture, plus a Python MCP server (`agileplus-mcp/`), an Electrobun desktop shell, a REST API
(`openapi.yaml`) and gRPC. Domain spine: `Feature → WorkPackage → Cycle/Module` (with `Story`/`Epic`/`Project` for backlog shape). Storage is
**SQLite** (ADR-003) with a SHA-256 hash-chained immutable audit log (ADR-004) and optional Plane.so / GitHub sync, Neo4j (ADR-013) and NATS JetStream
(ADR-006). Per `ARCHITECTURE.md:14` it is "the work-tracking spine for this shelf: work is described as specs and work packages, implemented in
repo-specific worktrees, then merged back to the owning canonical repo." CLI: `agileplus <command>` (`crates/agileplus-cli/src/main.rs`).

## Artifact layout

### 2.1 Canonical current layout — per-repo state contract

Authoritative: `docs/agileplus/repo-state-contract.md`; doc-comment `crates/agileplus-git/src/materialize.rs:1-14`.

```text
<repository>/
├── .agileplus/
│   ├── agileplus.db          # mutable local SQLite state
│   └── locks/ logs/ cache/   # ignored
└── docs/agileplus/<feature-slug>/
    ├── meta.json
    ├── status.md
    ├── audit.jsonl
    └── work-packages/<work-package-id>.json
```

"Files under `docs/agileplus/` are Git-tracked human and review artifacts… materialized from the repository-local source of truth; direct edits are
overwritten on the next materialization."

### 2.2 Write chokepoint

`VcsPort::write_artifact(feature_slug, relative_path, content)` → `<repo_root>/docs/agileplus/<slug>/<relative_path>`, and stages it in the git index
(`crates/agileplus-git/src/lib.rs:218`, `artifact/mod.rs:59`). Observed `relative_path` values: `spec.md` (`specify.rs:239`), `plan.md`
(`plan.rs:222`), `tasks/WP{:02}-{slug}.md` (`plan.rs:230`), `contracts/governance-v1.json` (`plan.rs:254`), `evidence/spec-revisions/rev-{n}.diff`
(`specify.rs:298`), plus from `materialize.rs:53,68,120,272`: `meta.json`, `status.md`, `audit.jsonl`, `work-packages/<wp_id>.json`.

### 2.3 Other artifact roots present (all legacy; four coexisting shapes)

`kitty-specs/` (≈75 dirs; `AGENTS.md:71` calls it "root-level spec archive (legacy, read-only)") · `docs/specs/` (001…007 + `eco/`, `crates/`) ·
`.archive/kitty-specs/` · `specs/epic_F/` · `tasks/WP11-stub/index.md` · `worklogs/*.json` · `worklog-L1-001-2026-06-11-canonical.json` (repo root) ·
`plans/agileplus-audit-130.json` · `spec/product/mature-contract.v1.json` · `verification/FORWARD_WBS_2026-10-04.md` · `.work-audit/` ·
`.agileplus/{agileplus.db,governance.db,logs/nats.log}`.

### 2.4 Real example tree — `kitty-specs/017-cli-tools-consolidation/`

```text
kitty-specs/017-cli-tools-consolidation/{spec.md, plan.md, tasks.md, meta.json}
```

- `spec.md` → `# CLI Tools Consolidation`, `## Meta` (`**ID**`, `**Title**`, `**Created**`, `**State**`, `**Scope**`), then `## Context / Problem
  Statement / Goals / Non-Goals`.
- `tasks.md` → `# Work Packages: <feature> — <subtitle>`, sections `## WP-001: <title>` carrying `- **State:** planned`, `- **Sequence:** 1`, `-
  **File Scope:**` (Read/Write lists), `- **Acceptance Criteria:**` (bulleted), `- **Estimated Effort:** L`, `### Subtasks` with `- [x] T001 …`.
- `plan.md` → `# Plan: <slug> — <subtitle>`, `## Phase N: <name>` sections, each a table `| WP | Description | Predecessors | Repos | Est. effort |`
  using `WP-000`/`WP-001` ids, `—` for none.
- `meta.json` here → `spec_id`, `slug`, `title`, `status`, `priority`, `created_at`, `source_description`. A **second** variant
  (`kitty-specs/eco-006-governance-sync/meta.json`) adds `feature_number`, `friendly_name`, `mission`, `target_branch`, `vcs`; a **third**
  (`docs/specs/…/meta.json`) differs again; a **fourth** (`specs/epic_F/tasks/WPF1.md`) is YAML frontmatter (`work_package_id`, `title`, `epic`,
  `lane`, `dependencies`, `created_at`, `subtasks`, `phase`, `assignee`, `agent`, `shell_pid`, `review_status`, `reviewed_by`, `history[]`; generator
  `run_dag_units.py`).

## Schemas

### 3.1 `Feature` — `crates/agileplus-domain/src/domain/feature.rs:58-74`

`id: i64` (AUTOINCREMENT) · `slug: String` (UNIQUE NOT NULL) · `friendly_name: String` · `state: FeatureState` · `spec_hash: [u8;32]` (SQLite BLOB;
SHA-256 of spec.md) · `target_branch: String` (default `main`) · `plane_issue_id`, `plane_state_id`: `Option<String>` · `labels: Vec<String>` ·
`module_id`, `project_id`: `Option<i64>` · `created_at_commit`, `last_modified_commit`: `Option<String>` · `created_at`, `updated_at`. `FeatureState`
(`crates/traceability-core/src/lifecycle.rs:17-26`, lowercase serde): `created, specified, researched, planned, implementing, validated, shipped,
retrospected`. Transitions are **strictly linear, one step**, self-transitions rejected (`feature.rs:95-117`); `retrospected` is terminal.

### 3.2 `WorkPackage` — `crates/agileplus-domain/src/domain/work_package/mod.rs:47-65`

`id: i64` · `feature_id: i64` (FK `features(id) ON DELETE CASCADE`) · `title` · `state: WpState` · `sequence: i32` (default 0; the human ordinal
`WP01`) · `file_scope: Vec<String>` (SQLite TEXT JSON array, default `'[]'`) · `acceptance_criteria: String` (free text, newline-separated) ·
`agent_id`, `pr_url`: `Option<String>` · `pr_state: Option<PrState>` · `worktree_path`, `plane_sub_issue_id`, `base_commit`, `head_commit`:
`Option<String>` · `created_at`, `updated_at`. `WpState` (snake_case): `planned, doing, review, done, blocked`; legal transitions (mod.rs:21-33)
`planned→doing|blocked`, `doing→review|blocked`, `review→done|doing`, `blocked→planned|doing`. `PrState`: `open, review, changes_requested, approved,
merged`.

### 3.3 `Story` / `Epic` / traceability

`Story` (`story.rs:71-92`): `id, epic_id, project_id, title, description, status: StoryStatus, points: Option<u32>, assignee_id, requirement_id,
created_at, updated_at`; `StoryStatus`: `todo, in_progress, review, done, blocked, cancelled`. `Epic` (`epic.rs:65-80`): `id, project_id, title,
description, status: EpicStatus, owner_id, requirement_id, …`; `EpicStatus`: `backlog, active, review, done, cancelled`. `Requirement`
(`crates/traceability-core/src/requirement.rs:124-139`): flattened `Artifact` + `status: RequirementStatus`, `priority: Option<u8>`
(0=critical…5=nice-to-have), `rationale`, `acceptance_criteria: Vec<String>`, `verification_method: Option<VerificationMethod>`; `RequirementStatus`:
`draft, proposed, approved, implemented, verified, deprecated, rejected`; `VerificationMethod`: `test, analysis, inspection, demonstration, review`.
`Artifact` (`artifact.rs:59-78`): `id: Uuid` v4, `project_id: Uuid`, `kind: ArtifactKind`, `title`, `description`, `external_id: Option<String>`
("foreign key mirror"), `metadata: BTreeMap<String, Value>`; `ArtifactKind`: `requirement, design, code, test, evidence, risk, rationale`. `Criterion`
(`contract.rs:60-67`): `id` (`AC-1`), `test_ref`, `evidence_ref`.

### 3.4 Materialized JSON

`docs/agileplus/<slug>/meta.json` (`materialize.rs:53-68`) — exact keys: `slug`, `friendly_name`, `state`, `target_branch`, `spec_hash` (64-char hex
**string**), `created_at`, `updated_at`, `created_at_commit`, `last_modified_commit`, `labels`, `plane_issue_id`, `materialized_at`.
`work-packages/<wp_id>.json` (`materialize.rs:129-148`): `id`, `feature_id`, `title`, `state`, `sequence`, `acceptance_criteria`, `file_scope`,
`agent_id`, `pr_url`, `pr_state`, `worktree_path`, `base_commit`, `head_commit`, `created_at`, `updated_at`, `materialized_at`. `audit.jsonl`:
`timestamp`, `action` (`"materialized"`), `slug`, `state`, `commit`. `contracts/governance-v1.json` = `GovernanceContract` (`governance.rs:56-70`):
`id`, `feature_id`, `version`, `rules[{transition, required_evidence[], policy_refs[]}]`, `bound_at`; `EvidenceType` (`governance.rs:75-82`):
`test_result, ci_output, review_approval, security_scan, lint_result, manual_attestation`.

### 3.5 `ImportBundle` — `crates/agileplus-import/src/manifest.rs` (the interchange format)

Serde structs, snake_case JSON keys, `#[serde(default)]` on every optional field, **unknown fields ignored** (test `manifest.rs:299`). Required:
`ImportProject.name`; `ImportFeature.friendly_name` + `.spec_content`; `ImportWorkPackage.title`; `ImportCycle.name` + `.start_date` + `.end_date`.
- `ImportBundle` — `projects[]`, `modules[]`, `features[]`, `cycles[]`
- `ImportProject` — `slug: Option<String>`, `name: String`, `description`, `features[]: Vec<ImportFeature>`
- `ImportModule` — `slug: Option<String>`, `friendly_name: String`, `description`, `parent_slug`
- `ImportFeature` — `slug: Option<String>`, `friendly_name: String`, **`spec_content: String`**, `state: FeatureState` (default `specified`),
  `target_branch`, `labels: Vec<String>`, `module_slug`, `project_id: Option<i64>`, `plane_issue_id`, `plane_state_id`, `work_packages[]`
- `ImportWorkPackage` — `title: String`, `acceptance_criteria: Option<String>`, `sequence: Option<i32>`, `file_scope: Vec<String>`, `state: WpState`
  (default `planned`), `agent_id`, `pr_url`, `pr_state`, `worktree_path`, `plane_sub_issue_id`, **`depends_on_sequences: Vec<i32>`**
- `ImportCycle` — `name: String`, `description`, `start_date`/`end_date: NaiveDate`, `state: CycleState` (default `draft`), `module_scope_slug`,
  `feature_slugs: Vec<String>`
- `ImportReport` (`report.rs:4-18`): `projects_created/updated`, `modules_created/updated`, `features_created/updated`, `cycles_created/updated`,
  `work_packages_created/updated`, `module_links_created`, `cycle_links_created`, `artifacts_written`, `audits_written`.

### 3.6 OKF v1.0 and the REST contract

OKF (`crates/agileplus-cli/src/commands/okf.rs:26-87`): `{ "okf": "1.0", "source_id`, `entities[{id,type,label,properties}]`,
`relations[{source,target,type,provenance}]`, `provenance{corpus,source_id}` }`; entity types `intent, acceptance, constraint, resource, state,
criteria, gate`; relation types `verified_by, bounded_by, grounds, requires, asserts`; corpora `forge, codex, claude-code, cursor, factory-droid` — a
**session/evidence** graph, not a work-package format. REST (`openapi.yaml`): `FeatureResponse` (line 36) required `[id, slug, name, state,
target_branch, created_at, updated_at]` — note `name` here vs `friendly_name` in the domain, and its `state` doc lists `in_progress`, which is **not**
a `FeatureState` (the OpenAPI prose is stale). `WorkPackageResponse` (line 76) required `[id, feature_id, title, state, sequence, acceptance_criteria,
created_at, updated_at]`.

## IDs and traceability

**Identity.** `features.id` / `work_packages.id` / `stories.id` / `epics.id` are AUTOINCREMENT ints — *never* portable. Portable keys: feature `slug`
(UNIQUE), WP `sequence` (ordinal → `WP01` in plan.md and `tasks/WP{:02}-*.md`; `plan.rs:52`), `ImportModule.slug` (falls back to
`Module::slug_from_name(friendly_name)`), `ImportCycle.feature_slugs`. **Other ids:** `RequirementId` = `FR-77` / `FR-001` / `FR-<uuid>`; `NfrId` =
`NFR-PERF-01`; `Criterion.id` = `AC-1`; `Artifact.id` = Uuid v4; legacy `spec_id` (`"017"`, `"eco-006"`), `feature_number` (`"006"`), dir slug
(`"017-cli-tools-consolidation"`); worklog `req_id` prefix discriminates `L#-#` (team sprint) from `L5-###` (phono-worklog-schema v2,
README.md:130-136, ADR-032); subtask `T001` is markdown-only. **Links:** `work_packages.feature_id → features.id`; `stories.epic_id → epics.id`;
`epics.project_id → projects.id`; `story_work_packages` links Story↔WP. WP→WP: `wp_dependencies(wp_id, depends_on, dep_type)`, `dep_type ∈ {explicit,
file_overlap, data}`, PK `(wp_id, depends_on)`; Rust `WpDependency` (`work_package/dependency.rs:15-20`). `DependencyGraph` (`dependency.rs:22-80`):
`add_edge`, `add_file_overlap_edges` (auto-derives edges from `file_scope` intersection), `ready_wps(&HashSet<i64> done)`, `has_cycle()`,
`execution_order() -> Vec<Vec<i64>>` (topological layers). Generic edges: `trace_links(from_kind, from_id, to_kind, to_id, link_type, note,
created_by, created_at)`, UNIQUE on the 4 key cols; 9 `from_kind`/`to_kind` values and 9 `link_type` values, enumerated in the migration. Ontology:
`intent_nodes`, `intent_edges`, `v_intent_graph`, `v_orphan_nodes`, `v_traceability_chain`. **Progress:** `WpProgressSummary`
(`domain/cycle/progress.rs:8-14`): `{total, planned, in_progress, done, blocked}` — **counts, never a percentage**; no percentage field exists in the
domain. Ready-set = `DependencyGraph::ready_wps(done_set)`, surfaced by `ap mvp next-ready` and `ap dag topology`. Feature-level progress is derived
from its WPs, not stored. **Acceptance:** a `candidate_ref` of form `git:<ref>` is required for acceptance (`domain/acceptance.rs`); WP
`base_commit`/`head_commit` plus feature `created_at_commit`/`last_modified_commit` give git provenance.

## CLI surface

`/Users/kooshapari/.local/bin/agileplus` v0.2.4 (no build in tree — `target/{release,debug}/agileplus` absent; only `--help` was run). Top level:
`cycle, list, specify, queue, module, research, plan, implement, validate, ship, retrospective, triage, rubric, dag, okf, mvp, dashboard, cockpit,
help`. Globals: `-v/--verbose...`, `--repo <REPO>`, hidden deprecated `--db`, `-V`. The source enum (`main.rs:50-101`) adds `evaluate` + `evidence`;
`research/plan/implement/evaluate/validate/ship/retrospective/triage` are `#[cfg(feature = "full-deps")]`. The installed 0.2.4 binary lacks
`evaluate`/`evidence` — treat the source enum as the target surface.
- `specify` — `--feature <slug>` `--from-file <path>` `--target-branch <b>` `--force` (`commands/specify.rs:22-38`)
- `list` — `--state <s>` `--json` (emits `{id, slug, name, state, created_at}`) (`commands/list.rs:12-31`)
- `research` / `plan` / `implement` — Specified→Researched / writes plan.md + tasks/WP*.md + contracts + WP rows + deps / Planned→Implementing
  (`commands/{research,plan,implement}.rs`)
- `validate` — `--feature <slug>` `--format markdown|json` `--skip-policies` `--output <f>` `--force` (`commands/validate.rs:53`)
- `mvp project create` — `--slug` `--name` `--description` (`commands/mvp.rs:76-90`); `mvp story create` / `mvp epic create` — story/epic CRUD; `mvp
  wp create` — `--story <id>` `--title <t>` `--file-scope <s>` `--acceptance <a>` `--seq <n>` (`mvp.rs:145-160`)
- `mvp dep add` — `--wp <id>` `--depends-on <id>` `--type explicit|file_overlap|data` (`mvp.rs:169-185`); `mvp transition` / `mvp next-ready` — `--wp
  <id>`|`--story <id>` + `--to <state>` / `--cycle <id>` `--json` (`mvp.rs:212-230`)
- `dag` — `pick, claim, release, heartbeat, done, dedup, dedup-explain, scan, topology, where, add`; `okf` — `validate, summarize, merge <PATH>`

**State gates (binding for any importer).** `plan` requires the feature be `Researched` (`plan.rs:67`; `Planned` preserved, `Specified`/`Created`
error); `implement` accepts `Planned|Implementing`; `validate` requires `Implementing` (`--force` logs a governance exception). Happy path: `specify →
research → plan → implement → validate → ship → retrospective`. **Known inconsistency:** `specify` prints `Spec written to:
docs/agileplus/{slug}/spec.md` (`specify.rs:245`) but the revision path prints `Diff saved to: kitty-specs/{slug}/{path}` (`specify.rs:324`) — wrong;
the real write is `docs/agileplus/<slug>/evidence/spec-revisions/rev-N.diff`.

## Storage

- **SQLite (primary)**: `<repo>/.agileplus/agileplus.db`, opened read-write; the path is always derived from the repo root (`--db` deprecated/hidden,
  `main.rs:38-41`). Live tables (48): `_migrations, api_keys, assignments, assignment_criteria, attempts, audit_log, backlog_items,
  channel_iterations, cycle_features, cycle_stories, cycles, device_nodes, epics, evaluation_criterion_results, evaluations, evidence,
  feature_acceptance_receipts, features, gate_results, governance_contracts, intent_edges, intent_graph_metadata, intent_nodes, metrics, modules,
  module_feature_tags, policy_rules, projects, promotion_journals, run_records, scope_status, snapshots, spec_revisions, stories, story_work_packages,
  sync_mappings, trace_links, users, v_intent_graph, v_orphan_nodes, v_traceability_chain, work_packages, worklog_entries, wp_dependencies` (all
  enumerated from the live DB, not the migration list).
- `.agileplus/governance.db` — present but **empty (0 tables)** here. UNKNOWN what writes it.
- **Migrations**: `crates/agileplus-sqlite/src/migrations/*.sql` — 40 files, `include_str!`-embedded, ordered by list position in
  `migrations/mod.rs:6-112` (keys `001_create_features` … `031_promotion_saga`; prefixes `022` and `025` are each reused by three files, so **ordering
  is positional, not numeric**). Binaries: `src/bin/migrate.rs`, `seed_db.rs`, `seed_requirements.rs`.
- **Postgres**: none for AgilePlus. `ARCHITECTURE.md:94` lists "SQLite/Postgres/Redis" only as a cross-repo pattern.
- **External sync, not local DB**: Plane.so (`crates/agileplus-plane`), GitHub (`crates/agileplus-github`), git VCS (`crates/agileplus-git`), NATS
  (`crates/agileplus-nats`, log `.agileplus/logs/nats.log`), Neo4j optional (`crates/agileplus-graph`, ADR-013).
- **Files (git-tracked)**: `docs/agileplus/<slug>/…` (§2.1). Materialization is **one-way SSOT → git** (`materialize.rs:1-3`) — hand edits are
  overwritten. **Audit**: `audit_log` table, hash chain (`prev_hash`, `hash`, `hash_entry()` in `domain/audit.rs`). **Stray**: repo root holds orphan
  `agileplus.db-shm` (32 KB) + `agileplus.db-wal` (0 B).

## Proposed conversion envelope

**Key finding: the envelope already exists.** `crates/agileplus-import` defines a manifest-driven ingest format (`ImportBundle`) whose design is
already anti-lock-in: it carries **natural keys** (`slug`, `sequence`, `depends_on_sequences`, `feature_slugs`) and **omits** every AUTOINCREMENT id.
Our Go CLI should emit this format, not invent one.

### 7.1 Target format — `ImportBundle` JSON

```json
{ "projects": [ { "slug": "my-project", "name": "My Project", "features": [
    { "friendly_name": "CLI Tools Consolidation", "slug": "017-cli-tools-consolidation",
      "spec_content": "# Specification: CLI Tools Consolidation\n…", "state": "planned",
      "target_branch": "main", "labels": ["infra"], "work_packages": [
        { "sequence": 1, "title": "Complete LLM proxy with 8+ providers", "state": "planned",
          "acceptance_criteria": "LLM proxy supporting 8+ providers\nRetry per provider",
          "file_scope": ["src/proxy/"], "depends_on_sequences": [] },
        { "sequence": 2, "title": "Wire proxy into the CLI", "depends_on_sequences": [1] } ] } ] } ],
  "modules": [], "features": [], "cycles": [] }
```

JSON keys are exactly the Rust field names (no `rename_all`); every optional key is omittable; unknown keys are ignored, so the format is
forward-compatible.

### 7.2 What the importer does with it

`agileplus_import::import_bundle(bundle, storage, vcs) -> Result<ImportReport>` (`crates/agileplus-import/src/importer/mod.rs:15`):
- feature slug = `spec.slug` ?? `Feature::slug_from_name(friendly_name)` (`importer/features.rs:29-32`); `spec_hash = sha256_bytes(spec_content)`
  (`:33`) — **we supply content, never a hash**; feature upserted by slug (`:64`); `state` updated only when `!= FeatureState::Created` (`:89-91`);
  audit entry written (`:96`); then `vcs.write_artifact(slug, "spec.md", spec_content)` (`:103`) and `meta.json` (`:106`) — **the importer writes the
  git artifacts for us**; `module_id` resolved from `module_slug` via the in-batch map or `get_module_by_slug` (`:37-50`).
- WP upserted by `(feature_id, sequence)`; `sequence` defaults to `index + 1` when omitted (`importer/work_packages.rs:22,32-33`);
  `depends_on_sequences` → `add_wp_dependency` with `DependencyType::Explicit` (`:86-101`); an unresolvable sequence is a hard error ("work package
  '{}' depends on unknown sequence {}").

### 7.3 Gap to close on our side

`agileplus-import` is an **orphan library**: no `src/bin/`, no `import` subcommand in `crates/agileplus-cli/src/main.rs` (only
`commands/queue/import.rs`, a different thing), no MCP tool, and **no other crate depends on it**. The format is ready but the ingestion path is not
wired. Three options, in preference order:
1. **Ask AgilePlus to add `agileplus import --bundle <file>`** wrapping `import_bundle` (≈50 lines, reuses the existing `StoragePort` + `VcsPort`
   wiring the CLI already has). Then our Go CLI just writes a JSON file. *SPECULATIVE — needs an AgilePlus-side change.*
2. **Ship a tiny Rust shim binary** in our repo that depends on `agileplus-import` and calls `import_bundle`. *SPECULATIVE — adds a Rust dependency to
   a Go toolchain.*
3. **Have the Go CLI write the SQLite rows directly.** *Rejected:* it hard-codes the schema and bypasses the audit-log + artifact-write path, which is
   exactly the lock-in we are avoiding.

### 7.4 Minimal-glue alternative (no importer at all)

Emit only a `spec.md` in the exact shape of the `agileplus specify --from-file` template (`commands/specify.rs:170-197`): `# Specification: {name}` /
`**Slug** | **Date** | **State**` / `## Problem Statement` / `## Target Users` / `## Functional Requirements` / `## Non-Functional Requirements` / `##
Constraints & Dependencies` / `## Acceptance Criteria`. Then `agileplus specify --feature <slug> --from-file spec.md` → `research` → `plan` generates
the WP decomposition, `plan.md`, `tasks/WP*.md`, `contracts/governance-v1.json` and the DB rows. **Zero AgilePlus changes**, but we lose control of
the WP decomposition. Constraint: `specify` derives `friendly_name` from the first `# ` heading, stripping a leading `"# Specification:"`; absent →
`"Unnamed Feature"` (`specify.rs:96-101`).

### 7.5 Rules for whatever we emit

1. **Never emit integer ids** (`features.id`, `work_packages.id`, `project_id`). Use `slug` / `sequence`. `ImportFeature.project_id: Option<i64>` is
   the one id-shaped hole — leave it `null` and carry the project via `ImportBundle.projects[].features[]` nesting instead.
2. **Dependencies are intra-feature `sequence` references** — never cross-feature, never cross-repo.
3. **`state` must be a legal enum value**: `FeatureState` (8 lowercase values, strictly linear transitions) / `WpState` (5) / `PrState` (5) /
   `CycleState`.
4. **Supply `spec_content`, never `spec_hash`** — the importer hashes it. **`acceptance_criteria` is one string**, not an array (join with `\n`).
5. **Do not emit** legacy `kitty-specs` fields (`spec_id`, `feature_number`, `mission`, `vcs`, `priority`, `source_description`) — no `features`
   column for them; they would be dropped.
6. **Do not hand-write `docs/agileplus/<slug>/meta.json`** — `materialize.rs` regenerates it one-way from the DB. (`spec.md`, `plan.md`, `tasks/*.md`,
   `contracts/*` are *not* auto-materialized, so authoring those by hand is safe — but the importer writes `spec.md` and `meta.json` itself anyway.)

## Open questions

1. **UNKNOWN** — `projects` table columns and the `Project` domain struct were not read; the `ImportProject` → `projects` mapping is inferred from the
   importer, not read from the schema.
2. **UNKNOWN** — `.agileplus/governance.db` is present but empty; nothing in the tree obviously writes it.
3. **UNKNOWN** — Is there one canonical feature-level progress function, or is it derived ad hoc per call site? No `feature_progress` symbol found;
   `dashboard.rs` only colorizes states.
4. **UNKNOWN / highest value** — Should `agileplus import` be added to the CLI (§7.3 option 1)? We should not silently pick option 2 (Rust shim) or 3
   (direct SQLite writes) without a decision.
5. **UNKNOWN** — Is the `kitty-specs/` / `docs/specs/` layout still writable, or strictly archival? `AGENTS.md:71` says "legacy, read-only", yet
   **four** `meta.json` shapes coexist (§2.4) and `kitty-specs/` holds ≈75 live-looking dirs.
6. **UNKNOWN** — `openapi.yaml` `FeatureResponse.state` documents `in_progress`, which is not a `FeatureState`. Is the API layer mapping states, or is
   the doc wrong?
7. **UNKNOWN** — `spec/product/requirements.semantic.v1.json` (67 FRs) and `verification/semantic-verification-cases.v1.json` (92 cases) are
   product-level contracts. Whether imported features bind to `FR-###` ids is unclear: `Story`/`Epic` have `requirement_id`, but `Feature` has **no**
   `requirement_id` column and `ImportFeature` has no requirement field.
8. **UNKNOWN** — `wp_dependencies.dep_type` is always `Explicit` on the import path, while `DependencyGraph::add_file_overlap_edges` can auto-derive
   `file_overlap` edges from `file_scope`. Whether re-import reconciles those auto-edges is untested.
9. **UNKNOWN** — Whether the Plane.so / GitHub sync adapters would replay imported features outward, and whether an import should populate
   `plane_issue_id` to avoid duplicate issues.
