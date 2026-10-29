# skills/dagrun

The agent-facing skill for `dagrun`: one markdown file that any harness can read.

| File | What it is |
| --- | --- |
| `SKILL.md` | Generated from `internal/skill.SkillMarkdown()`. Frontmatter plus the command table, plan format, the verify rule, and common mistakes. |
| `README.md` | This file. |

## Why one file

The CLI is identical under jcode, kcode, codex, forge, and helioslite, so the
skill is identical too. A per-harness variant would be a second description of
the same binary and would drift from it. The skill only describes commands that
exist, and the command table is rendered from `internal/skill.Commands()`, so a
renamed or added verb cannot leave the skill stale.

## Do not edit SKILL.md by hand

`internal/skill/skill_test.go` asserts the file on disk is byte-identical to
`SkillMarkdown()`. Editing the markdown alone fails the test. Change
`internal/skill/skill.go` and regenerate: print `skill.SkillMarkdown()` from a
throwaway module that `replace`s `github.com/KooshaPari/dagrun` to this checkout
and redirect it into `skills/dagrun/SKILL.md`.

```sh
# in a scratch dir outside the repo
mkdir -p genskill && cd genskill
# go.mod: module github.com/KooshaPari/dagrun/genskill,
#         require github.com/KooshaPari/dagrun v0.0.0,
#         replace github.com/KooshaPari/dagrun => /path/to/this/repo
# main.go: fmt.Print(skill.SkillMarkdown())
GOFLAGS=-mod=mod go run . > /path/to/this/repo/skills/dagrun/SKILL.md
```

The module path must sit under `github.com/KooshaPari/dagrun/` or Go refuses to
import the internal package.

## Installing

Copy or symlink the directory where the harness looks for skills:

```sh
ln -s "$PWD/skills/dagrun" ~/.jcode/skills/dagrun     # jcode
ln -s "$PWD/skills/dagrun" ~/.kcode/skills/dagrun     # kcode
ln -s "$PWD/skills/dagrun" ~/.codex/skills/dagrun     # codex
ln -s "$PWD/skills/dagrun" ~/.forge/skills/dagrun     # forge
ln -s "$PWD/skills/dagrun" ~/.helioslite/skills/dagrun # helioslite
```

The skill assumes `dagrun` is on `PATH`. Keep it under 150 lines: it is read
into an agent's context on every invocation, so padding costs tokens on every
turn.
