---
name: dagrun
description: Plan and execute a multi-step task as a verified DAG, where a node is done only when its declared artifacts verify.
---

# dagrun

dagrun is a work-DAG executor for coding agents. You write a plan of nodes, each naming the artifacts that prove it happened; dagrun decides what is ready, hands you or a spawned agent the prompt for it, verifies the artifacts, and records the result. The DAG is the trace, so there is no second task list to drift out of sync.

## Commands

| command | what it does | example |
| --- | --- | --- |
| `plan [FILE]` | validate a plan and install it as the run | `dagrun plan plan.yaml` |
| `next` | list the ready nodes, with the prompt to hand an agent | `dagrun next --json` |
| `run` | execute ready nodes by spawning agent harnesses | `dagrun run --workers 4` |
| `claim NODE` | take one node when you are the agent doing the work | `dagrun claim 1.2 --owner me` |
| `done NODE` | verify the node's artifacts, then record it as done | `dagrun done 1.2` |
| `explain [NODE]` | show why a node is ready, blocked, or failed | `dagrun explain 1.2` |
| `status` | show the tree and the progress through it | `dagrun status` |
| `validate` | re-check every node against the filesystem | `dagrun validate` |
| `trace` | the merged execution trace: what happened, as opposed to what the state file says | `dagrun trace --json` |
| `submit` | post the run to a Tracera instance; fails loud on any reported error | `dagrun submit --url http://localhost:3180 --token TRACERA_TOKEN` |
| `skill` | print this skill, so an agent can load it without running --help | `dagrun skill` |

## Plan format

```yaml
version: 1
run: my-run
artifacts_root: artifacts
max_parallel: 4
nodes:
  - id: "1.1"
    title: Freeze the scope
    check: "test -s artifacts/INTENT.md"   # optional, must be able to fail
    artifacts:
      - path: artifacts/INTENT.md
        min_bytes: 400
        must_contain: ["Acceptance Criteria"]
  - id: "1.2"
    deps: ["1.1"]
    title: Implement the thing
    artifacts:
      - path: artifacts/impl.md
```

## The one rule

A node is done only when its artifacts verify. `dagrun done NODE` checks the declared paths, sizes, and `must_contain` strings against the filesystem; if they do not hold it refuses and says which contract failed. To finish a node, satisfy its contract and call the command. Do not report completion any other way.

## Common mistakes

- Writing the artifacts and moving on instead of calling `dagrun done NODE`. The node stays in progress and its dependents never become ready.
- Giving two parallel nodes the same artifact path. Both write it, the last writer wins, and one node verifies a file the other authored. One artifact, one node.
- Declaring a check of `true`. It cannot fail, so it proves nothing; leave `check` out or make it assert something real.
- Using a command that cannot fail (`ls`, `echo`, `cat`), or one whose exit status you then ignore. A check that always passes is not a check.
- Treating `dagrun run` as the only path. When you are already the agent, `claim`, do the work, `done` is the loop.
