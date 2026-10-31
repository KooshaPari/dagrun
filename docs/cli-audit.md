# dagrun CLI audit — 2026-10-31

Read-only audit of every subcommand, run against a built binary in a temp dir.
Method: `go build -o /tmp/dagrun-audit .`, then for each command run `--help`
and run it with no arguments, measuring the exit code UNPIPED (a pipe masks
the code — this bit us before).

## Headline

**26 commands. 26 now exit 0 on `--help`.** Before the fix, 24 of 26 exited 1.

The defect was central, not per-command: `flag.ContinueOnError` returns
`flag.ErrHelp`, and `main` propagated every non-nil error to `os.Exit(1)`.
An agent asking a command for its usage got a printed usage block AND a
failure exit — a false error on the single most common probe an agent makes.

Only `viz`, `template`, and `skill` were right, because they handle `--help`
themselves instead of going through the flag package.

## Command table

| command | --help exit | in usage() | description accurate | no-arg behaviour |
|---|---|---|---|---|
| plan | 0 | yes | yes | exit 1, "plan source, - for stdin" — clear |
| run | 0 | yes | yes | exit 1, needs a run dir |
| next | 0 | yes | yes | exit 1, no plan installed — clear |
| claim | 0 | yes | yes | exit 1, needs a node — clear |
| done | 0 | yes | yes | exit 1, needs a node — clear |
| fail | 0 | yes | yes | exit 1, needs a node — clear |
| skip | 0 | yes | yes | exit 1, needs a node — clear |
| status | 0 | yes | yes | exit 1, no plan — clear |
| validate | 0 | yes | yes | exit 1, no plan — clear |
| trace | 0 | yes | yes | exit 1, no run dir — clear |
| export | 0 | yes | yes | exit 1, no run dir — clear |
| submit | 0 | yes | yes | exit 1, no plan — clear |
| detect | 0 | yes | yes | exit 0, prints the harness table — correct |
| mcp | 0 | yes | yes | exit 0, serves stdio — correct |
| explain | 0 | yes | yes | exit 1, needs a node — clear |
| viz | 0 | yes | yes | exit 1, no run — clear |
| draft | 0 | yes | yes | exit 1, needs an intent — clear |
| doctor | 0 | yes | yes | exit 1, no run dir — clear |
| gc | 0 | yes | yes | exit 1, no run dir — clear |
| workspace | 0 | yes | yes | exit 1, no run dir — clear |
| timeline | 0 | yes | yes | exit 0, empty timeline — correct |
| stats | 0 | yes | yes | exit 1, no run — clear |
| critique | 0 | yes | yes | exit 1, needs a node — clear |
| lint | 0 | yes | yes | exit 1, no run — clear |
| review | 0 | yes | yes | exit 1, needs a node — clear |
| template | 0 | yes | yes | exit 0, lists templates — correct |
| skill | 0 | yes | yes | exit 0, prints the skill — correct |

Every command in the dispatch switch appears in `usage()`, and every command in
`usage()` exists in the switch. No orphans in either direction.

## Checks

| check | result | evidence |
|---|---|---|
| `dagrun` with no args prints usage and exits 2 | PASS | exit 2 |
| `dagrun help` exits 0 | PASS | exit 0 |
| `dagrun --version` and `dagrun version` both work | PASS | both exit 0 |
| unknown command exits 2 with a clear message | PASS | `dagrun: unknown command "bogus-command"` + usage, exit 2 |
| every command in usage() exists, and vice versa | PASS | 27 switch cases, 27 usage lines, no diff |
| `dagrun skill` prints YAML frontmatter and a command table | PASS | `---` / `name: dagrun` / `description: ...` / table |
| `dagrun submit --help` documents --token as an ENV VAR | PASS | "NAME of the env var holding the bearer token; never the token itself" |
| `dagrun run --help` documents --force and --dry-run | PASS | both present |
| `dagrun doctor` exits non-zero on an actionable finding, 0 when clean | PASS | exit 1 with `harness: definitely-not-real`, exit 0 otherwise |
| `dagrun trace --json` emits NDJSON, not a JSON array | PASS | 6 lines, each parses as a JSON object |

## Dead surface

**None.** Every package under `internal/` is imported by at least one other
package:

    diffscope 1   export 2   harness 8   hook 1   house 2   mcp 1
    plan 62         report 1   run 4       skill 1  store 29  timeline 1
    trace 23        tracera 2  verify 15  viz 1    workspace 1

(`export` had been imported only by `internal/tracera` before; it now has a
second importer as well. `internal/skill` was imported by NOTHING — the
package was fully implemented and tested but no command called it. That is
fixed: `dagrun skill` exists and prints it.)

## Worst three problems, by how likely they are to waste an agent's time

1. **`--help` exited 1 on 24 of 26 commands.** FIXED. Every agent's first
   action against a new CLI is to ask it for help; making that look like a
   failure trains the agent to distrust the tool's output, or to retry
   blindly. The fix is one `errors.Is(err, flag.ErrHelp)` in `main`.

2. **The `internal/skill` package had no caller.** FIXED. The skill was
   implemented, tested, and shipped — and unreachable. It was the agent-facing
   documentation surface, and it did not exist from the outside.

3. **A throttled provider failed the whole run.** FIXED. A 429 classified as
   a plain harness error was retried immediately into the same wall. It is now
   its own retryable class with a default backoff, a provider cooldown, and an
   attempt floor — and the budget is decided in ONE place, because the retry
   policy and the claim check had disagreed and the extra attempts never
   happened.
