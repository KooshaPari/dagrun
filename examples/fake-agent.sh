#!/bin/sh
# A stand-in for a real agent, used only to demonstrate that dagrun's spawn
# path, artifact contract, and verification loop actually work end to end.
#
# It reads the prompt dagrun hands it, finds every "required output" path, and
# writes a placeholder that satisfies the declared predicate. A real harness
# (jcode / kcode / codex / forge) would do the actual work instead.
#
#   dagrun run --exec-template 'sh examples/fake-agent.sh {prompt}'
set -eu

prompt="${1:-}"
if [ -z "$prompt" ]; then
  echo "fake-agent: no prompt on argv" >&2
  exit 1
fi

body="$(printf '%s\n' "$prompt")"

# Extract the backticked paths from the "Required output" section.
paths="$(printf '%s\n' "$body" | sed -n 's/^- `\([^`]*\)`.*/\1/p')"
if [ -z "$paths" ]; then
  echo "fake-agent: no declared artifacts in prompt" >&2
  exit 1
fi

printf '%s\n' "$paths" | while IFS= read -r rel; do
  case "$rel" in
    *'*'*) continue ;;                 # globs are the caller's problem
  esac
  dir="$(dirname "$rel")"
  [ -d "$dir" ] || mkdir -p "$dir"
  {
    printf '# %s\n\n' "$rel"
    printf 'Placeholder produced by examples/fake-agent.sh.\n\n'
    printf '## Acceptance Criteria\n\n'
    i=0
    while [ "$i" -lt 40 ]; do
      printf -- '- criterion %s satisfied\n' "$i"
      i=$((i + 1))
    done
    printf '\n## Sources\n\n'
    printf -- '- https://example.invalid/placeholder\n'
    printf -- '- https://example.invalid/placeholder-2\n'
    printf '\nRequirement: placeholder content for the demo.\n'
    printf '#### Scenario: when the placeholder is read\n'
    printf 'then the demo is satisfied.\n'
    printf '\nDecision: use the placeholder.\n'
    printf 'Alternatives: none considered.\n'
    printf '\nVerdict: adopt, because the placeholder satisfies every predicate.\n'
    printf '\n## Candidates\n'
    printf -- '- **Option A** — simple\n'
    printf -- '- **Option B** — complete\n'
    printf '\n## Axes\n'
    printf -- '- axis: cost vs complexity\n'
    printf -- '- axis: speed vs memory\n'
    printf -- '- axis: build size vs runtime\n'
    printf '\nStates: empty, loading, error.\n'
    printf 'Accessibility: aria labels on every control.\n'
  } > "$rel"
  echo "fake-agent: wrote $rel ($(wc -c < "$rel" | tr -d ' ') bytes)"
done
