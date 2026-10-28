# E — Prior Art and User Feedback: Minimal "Work DAG Executor"

**Subject:** a minimal Go CLI that executes a task DAG and verifies that each node produced its required artifact.
**Compiled:** 2026-10-28
**Sibling reports (same directory):** `B-agent-orchestration-and-artifacts.md`, `D-go-concurrency-durable-state.md`, `F-agileplus-artifact-model.md`, `G-tracera-artifact-model.md`.

---

## Method and verification limits

- Repository metadata (stars, language, license, last push) was read from the GitHub REST API on 2026-10-28 for every repo named below. The repo URL is the citation for that metadata.
- HN quotes were read from the HN Algolia API (`https://hn.algolia.com/api/v1/items/<id>`) and are cited by **comment permalink**, not by thread.
- Reddit quotes were read by rendering `www.reddit.com` comment pages in a browser on 2026-10-28. Reddit's JSON API (`*.json`, `api.reddit.com`) returns HTTP 403, and `old.reddit.com` serves a "Welcome to Reddit" block page to non-browser clients. Two workarounds were used and are safe: Reddit's in-thread comment filter (`?q=<phrase>`) to surface a comment nested under a collapsed reply branch, and `reddit.com/r/<sub>/search.rss` to resolve thread permalinks.
- DuckDuckGo web search was anti-bot blocked, so general web discovery was unavailable. Discovery ran through the GitHub API, HN Algolia, Reddit (browser + RSS), and vendor documentation.
- `cased/cased` returns HTTP 404 from the GitHub API; the org now publishes `cased/kit`, `cased/sandboxes`, `cased/cli`, `cased/cased-agent`.
- No claim in this report comes from a truncated or unread source. Anything not directly verifiable is marked `UNVERIFIED`.
- Two README quotations come from a **Chinese-language** source ([pi-dag-core](https://github.com/Nuctori/pi-dag-core), 26% CJK characters). The original Chinese is quoted and the English is explicitly marked as a translation.
- One Reddit source ([u/saurabhjain1592](https://www.reddit.com/r/golang/comments/1t6jd7c/why_we_built_our_llm_workflow_runtime_in_go/)) is a **vendor's own post tagged "Brand Affiliate"**. It is cited as a design statement by a practitioner, and is not counted as independent user feedback.

**Verdict legend:** `ADOPT` use as-is · `FORK` fork and change · `REFERENCE` read for design, do not copy code · `IGNORE` wrong shape, wrong license, or dormant.

---

## Existing tools to adopt/fork

| Tool | ★ | Lang | License | Last push | Verdict | Why / gap |
|---|---|---|---|---|---|---|
| [anombyte93/prd-taskmaster](https://github.com/anombyte93/prd-taskmaster) | 605 | Python | MIT | 2026-08-14 | `FORK` (concept) | **Closest product.** Compiles a PRD into a "dependency-ordered task graph" and "executes every task with **verification evidence** — so 'done' means proven, not claimed", with "engine-enforced unfakable gates" (`validate-tasks`, evidence checks, `SHIP_CHECK_OK`). Python, and built around a Claude Code session. |
| [Nuctori/pi-dag-core](https://github.com/Nuctori/pi-dag-core) | 5 | TypeScript | MIT | 2026-10-06 | `REFERENCE` | DAG state machine with a "CI-style evidence chain": start proof, exit code, and artifacts are all observed by the core itself, not relying on AI self-report (README, Chinese — see the quoted original under *Reported failure modes*). Right idea, wrong language, 5 stars, bound to the `pi` runtime. |
| [yueheng-rgb/codex-factory](https://github.com/yueheng-rgb/codex-factory) | 92 | PowerShell | MIT | 2026-09-17 | `REFERENCE` | "a verification layer that treats real files, command exit codes, hashes, and receipts as evidence. **No artifact = no PASS.**" Closest prior art to artifact-gated DAG execution. Its own README declares its older release artifacts invalid, so it is a design reference, not a dependency. |
| [lacquerai/lacquer](https://github.com/lacquerai/lacquer) | 37 | Go | Apache-2.0 | 2025-08-22 | `FORK` | "a lightweight AI workflow engine that codifies repeatable engineering processes into reliable YAML workflows that **never skip a step**. Think GitHub Actions, but for AI-powered internal tools." Same language, same shape, same license — but 37 stars and last pushed 14 months ago. |
| [dagucloud/dagu](https://github.com/dagucloud/dagu) | 4,464 | Go | GPL-3.0 | 2026-10-27 | `REFERENCE` | "One binary, no database, runs on limited H/W resources", declarative YAML over scripts/SSH/containers. Proves the packaging story; GPL-3.0 blocks embedding in a permissive product. |
| [swaits/dagx](https://github.com/swaits/dagx) | 28 | Rust | MIT | 2026-02-20 | `REFERENCE` | "a minimal, type-safe, runtime-agnostic async DAG executor with **compile-time cycle prevention** and true parallel execution". Best reference for the executor core. |
| [reymom/rust-dag-executor](https://github.com/reymom/rust-dag-executor) | 11 | Rust | Apache-2.0 | 2026-03-12 | `REFERENCE` | "Std-only sync DAG executor with partial evaluation + bounded parallelism." A strong precedent for a zero-dependency CLI. |
| [microsoft/conductor](https://github.com/microsoft/conductor) | 483 | Python | MIT | 2026-10-23 | `REFERENCE` | Best "deterministic orchestration" reference: "Routing uses Jinja2 templates and expression evaluation. First matching condition wins. **No LLM in the orchestration loop**"; "Validation - Catches stale template references, missing inputs, and undeclared dependencies **before runtime**"; interactive DAG graph, in-browser human gates, OpenTelemetry traces. |
| [conductor-oss/conductor](https://github.com/conductor-oss/conductor) | 32,389 | Java | Apache-2.0 | 2026-10-27 | `IGNORE` | Durable execution at scale: "Every step is persisted. Survives crashes, restarts, and network failures with configurable retries and timeouts"; "restart, rerun, retry, pause, resume, or terminate". Right guarantees, wrong shape (server + database, Java). |
| [google/ax](https://github.com/google/ax) | 14,124 | Go | Apache-2.0 | 2026-10-27 | `REFERENCE` | "Agent Executor (AX) is Google's open agentic orchestration runtime". Best-positioned large incumbent: same language, permissive license. Agent-first rather than task-DAG-first, and far heavier than a minimal CLI. |
| [temporalio/temporal](https://github.com/temporalio/temporal) | 24,024 | Go | MIT | 2026-10-28 | `REFERENCE` | The durable-execution incumbent in the target language and license. "Every step is persisted. Survives crashes, restarts, and network failures"; built-in retries, timers, signals, and replay. Right guarantees, wrong shape: a server + database, not a single binary. Also the only tool in the survey to earn an explicit "use this instead" from a Go practitioner (see *User complaints*, quote 8). |
| [argoproj/argo-workflows](https://github.com/argoproj/argo-workflows) | 17,059 | Go | Apache-2.0 | 2026-10-27 | `REFERENCE` | Go + Apache-2.0 + actively developed, and the strongest existing answer to "Go DAG engine". But every step is a container: it is the opposite of a dependency-free local CLI, and it gates on pod success, not on a declared artifact. |
| [apache/airflow](https://github.com/apache/airflow) | 47,290 | Python | Apache-2.0 | 2026-10-27 | `IGNORE` | The most-complained-about orchestrator in the survey and the thing the target product is explicitly *not*. Python, scheduler + webserver + metastore. Useful as the canonical example of the complexity being avoided. |
| [dagster-io/dagster](https://github.com/dagster-io/dagster) | 16,341 | Python | Apache-2.0 | 2026-10-27 | `REFERENCE` | Best-in-class asset and observability model, and the source of two of the failure-mode citations below. Python, server-shaped, not a CLI. |
| [PrefectHQ/prefect](https://github.com/PrefectHQ/prefect) | 24,087 | Python | Apache-2.0 | 2026-10-27 | `IGNORE` | Explicitly the tool an engineer switched *to* to escape Airflow, so it is evidence for the complaint, not prior art for a minimal Go CLI. Python, server + database. |
| [Kocoro-lab/Shannon](https://github.com/Kocoro-lab/Shannon) | 2,303 | Go | MIT | 2026-09-05 | `REFERENCE` | "A production-oriented multi-agent orchestration framework" in Go under MIT. Good Go reference; no artifact-gated task DAG. |
| [mara/mara-pipelines](https://github.com/mara/mara-pipelines) | 2,091 | Python | MIT | 2023-12-15 | `REFERENCE` | "A lightweight opinionated ETL framework, **halfway between plain scripts and Apache Airflow**." The original statement of the gap this project targets; dormant since 2023, and Python. |
| [automazeio/ccpm](https://github.com/automazeio/ccpm) | 8,421 | Shell | MIT | 2026-10-19 | `REFERENCE` | Agent project management via GitHub Issues and git worktrees for parallel agent execution — the *board* side of the problem, not the *verify* side. |
| [astronomer/dag-factory](https://github.com/astronomer/dag-factory) | 1,464 | Python | Apache-2.0 | 2026-10-26 | `IGNORE` | Declarative YAML for Airflow DAGs. It is an Airflow front-end, so it inherits the Airflow deployment problem. |
| [kestra-io/kestra](https://github.com/kestra-io/kestra) | 30,039 | Java | Apache-2.0 | 2026-10-27 | `IGNORE` | Closest thing to Airflow-without-Python, and 30k stars of it. Java, server + database, plugin runtime. |
| [getaxonflow/axonflow](https://github.com/getaxonflow/axonflow) | 71 | Go | Business Source License (non-OSI) | 2026-10-28 | `IGNORE` | "the execution authority and system of record for AI decisions in production workflows", with a "Workflow Control Plane (WCP) … step-level gate checks, a durable step ledger, cancellation, and SSE streaming". The gate/ledger/cancellation design is exactly on target, but the license is a Business Source License with a non-compete additional-use grant, and it runs "self-hosted (Docker or Kubernetes)" — a server, not a binary. |
| [triggerdotdev/trigger.dev](https://github.com/triggerdotdev/trigger.dev) | 16,634 | TypeScript | Apache-2.0 | 2026-10-27 | `IGNORE` | Durable TS task engine, server-shaped. |
| [flyteorg/flyte](https://github.com/flyteorg/flyte) | 7,775 | Go | Apache-2.0 | 2026-10-27 | `REFERENCE` | Go + Apache + active, but Kubernetes-native and heavy. |
| [VRSEN/agency-swarm](https://github.com/VRSEN/agency-swarm) | 4,617 | Python | MIT | 2026-10-21 | `REFERENCE` | Source of the runaway-loop failure evidence below. |
| [openai/swarm](https://github.com/openai/swarm) | 22,079 | Python | MIT | 2026-04-15 | `IGNORE` | Educational multi-agent example, not an executor. |
| [luigi/luigi](https://github.com/luigi/luigi) | 1 | n/a | Apache-2.0 | 2014-07-11 | `IGNORE` | Dormant since 2014. |
| [cased/kit](https://github.com/cased/kit) | 1,310 | Python | MIT | 2026-03-03 | `IGNORE` | Agent sandboxing context, not orchestration. |

**Bottom line:** the "artifact as proof of completion" idea is already claimed by three small projects ([prd-taskmaster](https://github.com/anombyte93/prd-taskmaster), [codex-factory](https://github.com/yueheng-rgb/codex-factory), [pi-dag-core](https://github.com/Nuctori/pi-dag-core)) and the "small single-binary YAML DAG" shape by one dormant Go project ([lacquer](https://github.com/lacquerai/lacquer)). Go + MIT/Apache + actively developed is *not* an empty space — [Temporal](https://github.com/temporalio/temporal) (24,024★, MIT) and [Argo Workflows](https://github.com/argoproj/argo-workflows) (17,059★, Apache-2.0) both hold it — but both are server- or container-shaped, and neither gates a node on a *declared artifact*. No surveyed project is Go + MIT/Apache + actively developed + artifact-gated + dependency-free. The gap is real but narrow: the defensible position is **Go, single static binary, zero runtime dependencies, artifact-gated node completion, deterministic dependency graph**.

---

## Adjacent tools

| Tool | Problem it solves | Artifact model | Executes or plans? | Relationship |
|---|---|---|---|---|
| [github/spec-kit](https://github.com/github/spec-kit) (143,341, Python, MIT, 2026-10-27) | Turn an idea into a plan and a task list for SDD | `.specify/` artifacts: spec, plan, tasks; optional GitHub Actions workflow steps | Plans; can run a workflow step | **Complement** — produces the tasks a DAG executor would run |
| [Fission-AI/OpenSpec](https://github.com/Fission-AI/OpenSpec) (73,169, TypeScript, MIT, 2026-10-28) | Spec/change governance across repos | `openspec/` specs and changes, plus cross-repo "stores" | Plans only ("your AI writes these; you review the plan before any code is written") | **Complement** — spec layer |
| [bmad-code-org/BMAD-METHOD](https://github.com/bmad-code-org/BMAD-METHOD) (54,642, Python, MIT per LICENSE file, 2026-10-27) | Right-sized agentic delivery method | Markdown artifacts + agent personas; "Choose a Planning Path" | Plans and builds; no artifact gate | **Complement** — method layer |
| [spec-kitty/spec-kitty](https://github.com/spec-kitty/spec-kitty) (1,706, Python, MIT, 2026-10-27) | Governed SDD across many harnesses with review/merge state in git | `kitty-specs/` work packages with lanes `planned → in_progress → for_review → approved → done`; pipeline `spec → plan → tasks → next → review → accept → merge` | Orchestrates agents in isolated worktrees; gates on human review/accept/merge | **Complement** — closest workflow sibling, but gates on *review*, not *artifact* |
| [Kiro](https://kiro.dev/docs/specs/) (AWS; closed source — "licensed to you as 'AWS Content' under the AWS Customer Agreement", ©2026 Amazon.com, Inc., per [kiro.dev/license](https://kiro.dev/license/)) | IDE-resident spec → task execution | `requirements.md`, `design.md`, `tasks.md` | **Executes.** "Kiro builds a **dependency graph** of the tasks in your `tasks.md` and groups independent tasks into **waves**" | **Competitor** (closed, IDE-bound) — the wave/dependency model is the closest published analogue to the target design |
| [Tessl](https://tessl.io/) (closed source; commercial product. No public license or terms page — `/terms`, `/legal`, `/license`, `/terms-of-use` all returned HTTP 404 on 2026-10-28, while the site lists "Pricing" and "Book a Demo") | Agent enablement: skill registry, scheduled "loops", eval/cost/security control | Skills and loops, not a task graph | Executes loops on a schedule; no dependency graph | **Complement** — supply-side of agent context, not orchestration |
| [eyaltoledano/claude-task-master](https://github.com/eyaltoledano/claude-task-master) (28,272, JavaScript, "Task Master License" carrying MIT terms, 2026-04-28) | Decompose a PRD into numbered tasks with dependencies; expose over MCP/CLI | `tasks.json` with task dependencies and status | Executes its own task list | **Competitor** — planning-led, no artifact verification, 6 months stale |
| [BloopAI/vibe-kanban](https://github.com/BloopAI/vibe-kanban) (28,479, Rust, Apache-2.0, 2026-10-26) | Run many coding agents in parallel worktrees on a kanban board | Worktree sessions and board columns | Executes agents; no artifact validation | **Complement** |
| [smtg-ai/claude-squad](https://github.com/smtg-ai/claude-squad) (8,679, Go, AGPL-3.0, 2026-08-20) | Manage multiple agent terminal sessions | Per-agent worktrees | Executes | **Complement** — AGPL blocks linking |
| [stravu/crystal](https://github.com/stravu/crystal) (now Nimbalyst) (3,131, TypeScript, MIT, 2026-02-26) | Parallel agent sessions in git worktrees; compare approaches | Worktree sessions | Executes | **Complement** |
| [MrLesk/Backlog.md](https://github.com/MrLesk/Backlog.md) (7,176, TypeScript, MIT, 2026-10-27) | Human + agent collaboration inside a git repo | Markdown kanban as source of truth | Tracks; does not execute | **Complement** |
| [gemini-cli-extensions/conductor](https://github.com/gemini-cli-extensions/conductor) (3,773, Python, Apache-2.0, 2026-09-01) | SDD protocol for coding agents | `Context → Spec & Plan → Implement` artifacts | Plans | **Complement** |
| [microsoft/conductor](https://github.com/microsoft/conductor) (483, Python, MIT, 2026-10-23) | Deterministic multi-agent workflow execution | One YAML file of agents, prompts, routing, and validation | Executes; human gates in a web dashboard | **Competitor** — closest open competitor on "deterministic + pre-validated + human-gated" |
| [conductor-oss/conductor](https://github.com/conductor-oss/conductor) (32,389, Java, Apache-2.0, 2026-10-27) | Durable, inspectable agent execution | Versioned workflow graph; every step persisted | Executes durably | **Competitor** — server-backed, heavy |

---

## User complaints (quoted)

Eight representative complaint quotes, verbatim from the cited permalink. Reddit comments were read from rendered comment pages; where a comment sits under a collapsed reply branch it was surfaced with Reddit's in-thread comment filter (`?q=`). The counter-evidence table below is deliberately not a complaint.

**On heavyweight frameworks being the wrong tool**

1. > "if you're just running dbt itself on schedule you don't need a fancy orchestrator, you can do it with your clouds chosen scheduler service, your servers systemd timer, or your cicds providers scheduled job functionality."
   — u/adapdergentlefolk, [r/dataengineering, "Self Hosting DBT Core: What Orchestrators Do We Like?"](https://www.reddit.com/r/dataengineering/comments/1uej2uv/self_hosting_dbt_core_what_orchestrators_do_we_like/)

2. > "Airflow is a python framework like any other. If you can't make it do what you want, you have no business writing your own orchestrator from scratch. Hubris."
   — u/mRWafflesFTW, [r/dataengineering, "Lead wants to write our own orchestrator"](https://www.reddit.com/r/dataengineering/comments/1f157tn/lead_wants_to_write_our_own_orchestrator/) (thread OP: "Our team currently uses airflow as our data pipeline orchestrator. We have some fairly complex job dependencies and 100+ DAGs … Just venting. Some engineers always want to build things just to build things.")

3. > "Use AWS, Azure, Databricks, Snowflake, Kafka, Spark, DBT, Airflow.  Add skills to resume.  Review costs.  Reduce costs.  Remove Airflow, remove dbt, remove spark, remove kafka, remove snowflake, remove databricks.  Accept promotion for reducing costs.  Leverage promotion for better position in new company."
   — u/Lower_Sun_7354, [r/dataengineering, "Your dream data Architecture"](https://www.reddit.com/r/dataengineering/comments/1e3y2re/your_dream_data_architecture/)

**On learning curve and operability**

4. > "at my last startup I asked a friend to help me debug an Airflow DAG. he just pip installed prefect and I've never really looked back. at the time everything else felt too hard to figure out."
   — [itfollowsthen on HN](https://news.ycombinator.com/item?id=43440723) (thread: [Ask HN: What is the simplest data orchestration tool you've worked with?](https://news.ycombinator.com/item?id=43439939))

5. > "There's a reason it's the bread and butter starting point! My problem is the learning curve to get everything setup and break fixes along the way… I'm our defacto DE because I'm the only one who knew enough to be dangerous and I'm not afraid to break things if they can be easily fixed or rolled back"
   — u/Apprehensive-Ad-80, [r/dataengineering, "Self Hosting DBT Core: What Orchestrators Do We Like?"](https://www.reddit.com/r/dataengineering/comments/1uej2uv/self_hosting_dbt_core_what_orchestrators_do_we_like/)

6. > "the reality of most pipelines is that if you're using any sort of framework like Apache airflow or dagster you're just testing the framework itself which, while of some value, given the need to keep these frameworks up to date and the need to ensure breaking changes don't affect your code, don't really help you validate the day to day running of your pipeline."
   — u/AltruisticWaltz7597, [r/dataengineering, "Interviewer keeps praising me because I wrote tests"](https://www.reddit.com/r/dataengineering/comments/1lhgnlz/interviewer_keeps_praising_me_because_i_wrote_tests/) — same comment, continued: "Instead, integration tests and stress tests are much more important. You often need to ensure your pipelines will work (or at least fail gracefully) with bad data and not hang with huge amounts of data."

**On the shape of the tool people actually want**

7. > "…but why? Just use airflow. Things I want in an ETL: [x] works at scale. [x] simple to use. [x] not written in python (eg. in go or rust) [x] easy to scale (eg. in docker) [ ] this."
   — [shadowmint on HN](https://news.ycombinator.com/item?id=17030282) (thread: [Mara: A lightweight ETL framework, halfway between plain scripts and Airflow](https://news.ycombinator.com/item?id=17030102))

8. > "A workflow calls an LLM, touches a real system, partially succeeds, then retries. Now you need to answer three boring but painful questions: what actually ran / what got cancelled / what can safely be replayed"
   — u/saurabhjain1592 (Go runtime author; **vendor post**, tagged "Brand Affiliate"), [r/golang, "Why we built our LLM workflow runtime in Go instead of Python"](https://www.reddit.com/r/golang/comments/1t6jd7c/why_we_built_our_llm_workflow_runtime_in_go/) — same post: "Single static binaries helped too." and "If 10 callers hit the same workflow step at once, they need to converge on the same persisted decision, not 10 local interpretations of what probably happened."

**Counter-evidence — the heavyweight side wins in specific cases** (recorded so this report is not one-sided):

| Voice | Point | Link |
|---|---|---|
| saimiam (HN) | "there is no chance I would be able to orchestrate some of the workflows I want to orchestrate if it were not for Step Functions. For a one person shop like me, AWS is a force multiplier." | [HN 31482086](https://news.ycombinator.com/item?id=31482086) |
| u/MadT3acher | "We use self hosted Airflow with dbt. It's not the stuff that Reddit revs about but it gets the job done honestly" | [r/dataengineering](https://www.reddit.com/r/dataengineering/comments/1uej2uv/self_hosting_dbt_core_what_orchestrators_do_we_like/) |
| speedgoose (HN) | "Argo Workflow has been a pretty reliable plugin to Kubernetes for the more advanced scenarios. However, it's simple only if you are already familiar with software containers and Kubernetes." | [HN 43441391](https://news.ycombinator.com/item?id=43441391) |
| u/Icy_Peanut_7426 | "Agreed. We use Databricks, works like a charm" | [r/dataengineering](https://www.reddit.com/r/dataengineering/comments/1uej2uv/self_hosting_dbt_core_what_orchestrators_do_we_like/) |
| u/minormisgnomer | "Orchestrators shouldn't be deployed on a project basis, it's a company/department decision." … "Realistically there is no master DAG." | [r/dataengineering, "At what point do you say orchestrator (e.g. Airflow) is worth added complexity?"](https://www.reddit.com/r/dataengineering/comments/1g16hcd/at_what_point_do_you_say_orchestrator_eg_airflow/) |

**Design boundary drawn by the data:** u/Choperello, same thread — "Orchestration is a need you have when complexity in dependencies and chain of execution grows in complexity … the most common cases when you NEED something like airflow or similar arise at the company level or large dept level." A single-node-or-few-nodes local executor is the right answer *below* that threshold, and must not pretend to be a company-wide scheduler.

---

## What people actually want

Seven ranked properties. Each is anchored to at least one independent voice above; ranking is by how often the property appears as an explicit, stated requirement rather than an inference.

1. **A single local binary with no server and no database.** Explicitly requested: "Ie has dependencies, no database, flat files, single instance" ([rr808 on HN](https://news.ycombinator.com/item?id=31481861)); "you can do it with your clouds chosen scheduler service, your servers systemd timer" ([u/adapdergentlefolk](https://www.reddit.com/r/dataengineering/comments/1uej2uv/self_hosting_dbt_core_what_orchestrators_do_we_like/)); "good core dev-friendly tools" ([luminousbit on HN](https://news.ycombinator.com/item?id=44985138)); "Single static binaries helped too" ([u/saurabhjain1592](https://www.reddit.com/r/golang/comments/1t6jd7c/why_we_built_our_llm_workflow_runtime_in_go/)). Market-proven by [dagu](https://github.com/dagucloud/dagu) ("One binary, no database") and [lacquer](https://github.com/lacquerai/lacquer) ("a single Go binary").
2. **A declarative dependency graph that the tool derives, not the user hand-writes.** Kiro's published behaviour is the reference implementation of the mental model: it "builds a **dependency graph** of the tasks in your `tasks.md` and groups independent tasks into **waves**" ([Kiro docs](https://kiro.dev/docs/specs/)). Corroborated by [prd-taskmaster](https://github.com/anombyte93/prd-taskmaster) ("dependency-ordered task graph") and [microsoft/conductor](https://github.com/microsoft/conductor) (validation "catches … undeclared dependencies **before runtime**").
3. **Artifact-as-proof: "done" means an artifact exists, not that a process exited 0.** This is the differentiator the target product is built on, and it is already the explicit pitch of the closest prior art: "executes every task with **verification evidence** — so 'done' means proven, not claimed" ([prd-taskmaster](https://github.com/anombyte93/prd-taskmaster)); "**No artifact = no PASS**" ([codex-factory](https://github.com/yueheng-rgb/codex-factory)); evidence "observed by the core itself, not relying on AI self-report" ([pi-dag-core](https://github.com/Nuctori/pi-dag-core), Chinese README, translated); workflows that "**never skip a step**" ([lacquer](https://github.com/lacquerai/lacquer)). The user-side equivalent is u/AltruisticWaltz7597: what a pipeline needs is not unit tests of the framework but proof that it "will work (or at least fail gracefully) with bad data".
4. **Resume from the failed node, not rerun the whole pipeline.** u/Sex4Vespene: "if something fails, you can have it just kick off the rerun from the failed model instead of rerunning the whole package" ([r/dataengineering](https://www.reddit.com/r/dataengineering/comments/1uej2uv/self_hosting_dbt_core_what_orchestrators_do_we_like/)). Corroborated by the request for a "`wait/resume` for long running jobs" ([zubairov on HN](https://news.ycombinator.com/item?id=37753461)), by [conductor-oss/conductor](https://github.com/conductor-oss/conductor) making "restart, rerun, retry, pause, resume, or terminate" a first-class operation, and by the practitioner's framing "what actually ran / what got cancelled / **what can safely be replayed**" ([u/saurabhjain1592](https://www.reddit.com/r/golang/comments/1t6jd7c/why_we_built_our_llm_workflow_runtime_in_go/)).
5. **Idempotency and determinism by default, not by discipline.** "They both pay lip service to idempotent workflows, but doing the heavy lifting of making your workflows idempotent is up to you" ([andscoop on HN](https://news.ycombinator.com/item?id=17035933)). The concurrency requirement is stated precisely by a Go runtime author: "If 10 callers hit the same workflow step at once, they need to **converge on the same persisted decision, not 10 local interpretations of what probably happened**" ([u/saurabhjain1592](https://www.reddit.com/r/golang/comments/1t6jd7c/why_we_built_our_llm_workflow_runtime_in_go/)). The design-rule form of the same requirement: "Routing uses Jinja2 templates and expression evaluation. First matching condition wins. **No LLM in the orchestration loop**" ([microsoft/conductor](https://github.com/microsoft/conductor)).
6. **Language-neutral, and specifically not Python-bound.** "[x] not written in python (eg. in go or rust)" ([shadowmint on HN](https://news.ycombinator.com/item?id=17030282)); "I don't see the attraction of a language-specific framework. I don't use Python, and adding yet another language into the mix would just add complexity" ([atombender on HN](https://news.ycombinator.com/item?id=31490898)).
7. **Visible status and a human escape hatch.** "If a task gets stuck running due to pod OOM errors then the DAG disappears from the UI" ([apache/airflow#55029](https://github.com/apache/airflow/issues/55029)); a materialize button that "did nothing, no runs launched, no error toasts" ([dagster-io/dagster#22145](https://github.com/dagster-io/dagster/issues/22145)). Kiro "provides a task execution interface for tasks.md files that displays **real-time status updates**" ([Kiro docs](https://kiro.dev/docs/specs/)); [spec-kitty](https://github.com/spec-kitty/spec-kitty) makes review/accept/merge explicit lanes; u/minormisgnomer describes the operator payoff — "I wake up every morning, see a dashboard of completed jobs and know I'll probably have a decent day at work" ([r/dataengineering](https://www.reddit.com/r/dataengineering/comments/1g16hcd/at_what_point_do_you_say_orchestrator_eg_airflow/)).

---

## Reported failure modes

Each mode is anchored to a filed bug or a quoted complaint. "Minimal mitigation" is deliberately scoped to a small CLI — no database, no server, no plugin system. The pi-dag-core citations are to a Chinese README; the original text is quoted and the English is a translation.

| # | Failure mode | Evidence | Minimal mitigation |
|---|---|---|---|
| 1 | **Duplicate work** — the same node re-enqueued and re-executed | [PrefectHQ/prefect#22139](https://github.com/PrefectHQ/prefect/issues/22139): `MarkLateRuns` "enqueues … **without a deterministic dedup key**", and the policy "does not reject Late→Late", so redundant tasks each emit a fresh state event. Go runtime author: "If 10 callers hit the same workflow step at once, they need to converge on the same persisted decision, not 10 local interpretations of what probably happened" ([u/saurabhjain1592](https://www.reddit.com/r/golang/comments/1t6jd7c/why_we_built_our_llm_workflow_runtime_in_go/)) | Give every node a **stable content-addressed ID**; make the artifact registry the dedup key; skip a node whose verified artifact already exists; reject illegal state transitions (Late→Late) at the core |
| 2 | **Runaway loop / infinite recursion** | [VRSEN/agency-swarm#181](https://github.com/VRSEN/agency-swarm/issues/181) "Infinite loop made by the Genesis Agency"; [swaits/dagx](https://github.com/swaits/dagx) solves it with "compile-time cycle prevention"; [pi-dag-core](https://github.com/Nuctori/pi-dag-core) (README, Chinese): "「`maxIterations` 硬顶，不靠 AI 数数」" — translated: "a hard `maxIterations` ceiling, not relying on the AI to count" | **Parse-time cycle detection** on the dependency graph, plus a hard node/iteration budget and per-node timeout; abort with a non-zero exit and name the offending cycle |
| 3 | **Silent node failure** — work "succeeds" while nothing ran | [dagster-io/dagster#22145](https://github.com/dagster-io/dagster/issues/22145): missing env var → "clicking the Materialize button did nothing, no runs launched, no error toasts"; the whole thing "failed silently". u/AltruisticWaltz7597: a pipeline must "work (or at least fail gracefully) with bad data and not hang with huge amounts of data" ([thread](https://www.reddit.com/r/dataengineering/comments/1lhgnlz/interviewer_keeps_praising_me_because_i_wrote_tests/)). [pi-dag-core](https://github.com/Nuctori/pi-dag-core) (README, Chinese) enumerates adversarial cases it must survive: "「跳步（无执行即 complete）、改 payload、假产物、过期产物 … 循环耗竭」" — translated: "skip-step (complete with no execution), tampered payload, fake artifact, expired artifact … loop exhaustion" | **Fail closed.** Non-zero exit on any unverified node, capture stdout/stderr into the receipt, and never treat a missing artifact as success. Verify artifact freshness, not just existence. Error surface on stderr, machine-readable on stdout |
| 4 | **Lost state / dropped events on restart or failover** | [temporalio/temporal#11932](https://github.com/temporalio/temporal/issues/11932): on NDC conflict resolution "the loser state is rebuilt by replaying only the winner branch", and the reapply paths "allowlist one event type only", so "non-signal events" are "silently drop[ped]". [pi-dag-core](https://github.com/Nuctori/pi-dag-core) (README, Chinese) answers with an append-only event stream ("「事件流」") and a journal-backed state machine; its checkpoints are explicitly human-only ("「checkpoint 只能人解锁」" — "a checkpoint can only be unlocked by a human") | **Append-only journal**; atomic checkpoint per node completion; every state transition written before it is acted on; on resume, rebuild from the journal, never from an in-memory snapshot |
| 5 | **Stuck task with no visible completion** | [apache/airflow#17901](https://github.com/apache/airflow/issues/17901): "kubectl task gets stuck running if command takes more than 0.4 sec"; [apache/airflow#55029](https://github.com/apache/airflow/issues/55029): "If a task gets stuck running due to pod OOM errors then the DAG disappears from the UI". [pi-dag-core](https://github.com/Nuctori/pi-dag-core) (README, Chinese) ships a read-only stall detector: "「`stallAfterSec`（默认 600）」" — "stallAfterSec, default 600" — and "「不失败/不重试/不过期」" — "does not fail, does not retry, does not expire" | Emit a **per-node completion receipt** (artifact path, content hash, exit code, timestamp) and an aggregate run receipt; expose an explicit `stuck`/`running` state with a last-heartbeat field; never drop a node from the status view |
| 6 | **Framework churn and breaking-change tax** | u/AltruisticWaltz7597: "the need to keep these frameworks up to date and the need to ensure breaking changes don't affect your code" ([thread](https://www.reddit.com/r/dataengineering/comments/1lhgnlz/interviewer_keeps_praising_me_because_i_wrote_tests/)); u/Apprehensive-Ad-80 on Airflow's "learning curve to get everything setup and break fixes along the way" ([thread](https://www.reddit.com/r/dataengineering/comments/1uej2uv/self_hosting_dbt_core_what_orchestrators_do_we_like/)) | **Zero runtime dependencies** beyond the standard library, as [rust-dag-executor](https://github.com/reymom/rust-dag-executor) does with "Std-only"; one binary; no schema migrations because there is no schema to migrate |
| 7 | **Language lock-in** | "[x] not written in python (eg. in go or rust)" ([shadowmint on HN](https://news.ycombinator.com/item?id=17030282)); "adding yet another language into the mix would just add complexity" ([atombender on HN](https://news.ycombinator.com/item?id=31490898)) | The executor owns no application logic: it **runs arbitrary commands** and checks their declared artifacts, so node bodies can be in any language |
| 8 | **Credential and platform sprawl** | u/Lower_Sun_7354: "Use AWS, Azure, Databricks, Snowflake, Kafka, Spark, DBT, Airflow.  Add skills to resume.  Review costs.  Reduce costs.  Remove Airflow, remove dbt, remove spark, remove kafka, remove snowflake, remove databricks" ([thread](https://www.reddit.com/r/dataengineering/comments/1e3y2re/your_dream_data_architecture/)) | **Pass-through environment only.** The tool stores no credentials and implements no cloud integrations; the user chooses the platform, the tool only schedules and verifies |

---

## Sources

**GitHub repositories** (metadata via `gh api repos/<owner>/<repo>`, 2026-10-28; READMEs via `gh api repos/<owner>/<repo>/readme`)

- https://github.com/anombyte93/prd-taskmaster
- https://github.com/Nuctori/pi-dag-core
- https://github.com/yueheng-rgb/codex-factory
- https://github.com/lacquerai/lacquer
- https://github.com/dagucloud/dagu
- https://github.com/swaits/dagx
- https://github.com/reymom/rust-dag-executor
- https://github.com/microsoft/conductor
- https://github.com/conductor-oss/conductor
- https://github.com/google/ax
- https://github.com/temporalio/temporal
- https://github.com/argoproj/argo-workflows
- https://github.com/apache/airflow
- https://github.com/dagster-io/dagster
- https://github.com/PrefectHQ/prefect
- https://github.com/kestra-io/kestra
- https://github.com/getaxonflow/axonflow
- https://github.com/Kocoro-lab/Shannon
- https://github.com/mara/mara-pipelines
- https://github.com/automazeio/ccpm
- https://github.com/astronomer/dag-factory
- https://github.com/triggerdotdev/trigger.dev
- https://github.com/flyteorg/flyte
- https://github.com/VRSEN/agency-swarm
- https://github.com/openai/swarm
- https://github.com/luigi/luigi
- https://github.com/cased/kit
- https://github.com/github/spec-kit
- https://github.com/Fission-AI/OpenSpec
- https://github.com/bmad-code-org/BMAD-METHOD
- https://github.com/spec-kitty/spec-kitty
- https://github.com/eyaltoledano/claude-task-master
- https://github.com/BloopAI/vibe-kanban
- https://github.com/smtg-ai/claude-squad
- https://github.com/stravu/crystal
- https://github.com/MrLesk/Backlog.md
- https://github.com/gemini-cli-extensions/conductor

**GitHub issues**

- https://github.com/dagster-io/dagster/issues/22145
- https://github.com/PrefectHQ/prefect/issues/22139
- https://github.com/temporalio/temporal/issues/11932
- https://github.com/VRSEN/agency-swarm/issues/181
- https://github.com/apache/airflow/issues/17901
- https://github.com/apache/airflow/issues/55029

**Hacker News** (Algolia API, `https://hn.algolia.com/api/v1/items/<id>`)

- Threads: https://news.ycombinator.com/item?id=17030102 · https://news.ycombinator.com/item?id=31480320 · https://news.ycombinator.com/item?id=43439939 · https://news.ycombinator.com/item?id=44985058 · https://news.ycombinator.com/item?id=37750763
- Comments: https://news.ycombinator.com/item?id=17030282 · https://news.ycombinator.com/item?id=17035933 · https://news.ycombinator.com/item?id=31481861 · https://news.ycombinator.com/item?id=31482086 · https://news.ycombinator.com/item?id=31490898 · https://news.ycombinator.com/item?id=43440723 · https://news.ycombinator.com/item?id=43441391 · https://news.ycombinator.com/item?id=44985138 · https://news.ycombinator.com/item?id=37753461

**Reddit** (rendered pages, 2026-10-28)

- https://www.reddit.com/r/dataengineering/comments/1uej2uv/self_hosting_dbt_core_what_orchestrators_do_we_like/
- https://www.reddit.com/r/dataengineering/comments/1f157tn/lead_wants_to_write_our_own_orchestrator/
- https://www.reddit.com/r/dataengineering/comments/1e3y2re/your_dream_data_architecture/
- https://www.reddit.com/r/dataengineering/comments/1lhgnlz/interviewer_keeps_praising_me_because_i_wrote_tests/
- https://www.reddit.com/r/dataengineering/comments/1g16hcd/at_what_point_do_you_say_orchestrator_eg_airflow/
- https://www.reddit.com/r/golang/comments/1t6jd7c/why_we_built_our_llm_workflow_runtime_in_go/

**Vendor documentation**

- https://kiro.dev/docs/specs/ — dependency graph + wave execution model + real-time status
- https://kiro.dev/license/ — closed source; "licensed to you as 'AWS Content' under the AWS Customer Agreement", ©2026 Amazon.com, Inc.
- https://tessl.io/ — agent enablement platform (skills / loops / factory); commercial, no public license page
- https://github.com/getaxonflow/axonflow — "Workflow Control Plane (WCP) … durable step ledger, cancellation" and a "Decision record and evidence layer for replay"

**Unresolved / partially resolved sources**

- `https://github.com/cased/cased` — HTTP 404 on 2026-10-28; the requested repository does not exist under that path.
- `https://github.com/jinja-ai/mara` — HTTP 404 on 2026-10-28; the canonical Mara repository resolves to `https://github.com/mara/mara-pipelines`.
- Reddit JSON endpoints (`reddit.com/*.json`, `api.reddit.com`) and `old.reddit.com` — blocked to non-browser clients on 2026-10-28; all Reddit citations above were read from the rendered page.
- `tessl.io/terms`, `tessl.io/legal`, `tessl.io/license`, `tessl.io/terms-of-use` — all HTTP 404 on 2026-10-28. Tessl's licence terms are therefore `UNVERIFIED`; only the fact that the product is commercial (the site advertises Pricing and "Book a Demo") is verified.
