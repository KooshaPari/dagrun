# Research reports

Raw subagent output, kept verbatim so the reasoning is auditable and reusable.
These are inputs to `docs/research.md`, not documentation of the shipped tool.

| file | lane | state |
|---|---|---|
| `B-agent-orchestration-and-artifacts.md` | agent orchestration + spec-driven artifact models | complete |
| `D-go-concurrency-durable-state.md` | Go concurrency, SQLite vs flock, leases, supervision | complete, with measurements run on this host |
| `E-prior-art-and-user-feedback.md` | prior art, competitors, quoted user complaints | complete |
| `F-agileplus-artifact-model.md` | AgilePlus artifact model and `ImportBundle` | complete |
| `G-tracera-artifact-model.md` | Tracera trace/artifact model and gap analysis | complete |

Two planned lanes produced nothing:

- **A — Go DAG/workflow engine landscape.** The subagent failed twice on provider
  stream errors. Substance was recovered from lane E plus direct knowledge; see
  `docs/research.md` § Landscape.
- **C — harness detection and dispatch.** Also failed twice. Redone by hand from
  `--help` output on this machine; the result is `docs/harness-notes.md`.

If either lane is re-run later, drop the report here and reconcile it against
`docs/research.md` rather than appending.
