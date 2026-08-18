#!/usr/bin/env bash
# Extract the nexus3 CLI surface from source, with file:line for every claim.
#
# Help is partial (bare `nexus3` lists commands on stderr; flag.FlagSet verbs
# answer --help; hand-rolled groups like `sandbox` answer nothing), and flag
# parsing is not uniform: some
# commands declare flags via flag.FlagSet (fs.Bool("pty", ...)), others match
# literal "--flag" strings by hand. This script captures BOTH forms so the
# inventory cannot silently miss a surface just because a command parses
# arguments differently from its neighbours.
#
# Ground truth is source, not documentation. Run from the repo root:
#   scripts/docs/extract-surface.sh > surface-inventory.md
set -uo pipefail

CLI_DIR="${1:-internal/cli}"

printf '# nexus3 CLI surface inventory\n\n'
printf 'Generated: %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
printf 'Source: `%s` at commit `%s`\n\n' "$CLI_DIR" "$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
printf 'Every entry carries file:line. Regenerate with `scripts/docs/extract-surface.sh`.\n'

printf '\n## Registered commands\n\n'
printf 'Declared via `Register(Command{...})` in an `init()` per file (registry.go:10-23).\n\n'
grep -rn 'Name:[[:space:]]*"' "$CLI_DIR"/cmd_*.go 2>/dev/null \
  | grep -v '_test.go' \
  | sed -E 's/^([^:]+):([0-9]+):[[:space:]]*Name:[[:space:]]*"([^"]+)".*/- `\3` — \1:\2/' \
  | sort -u

printf '\n## Flags per command file\n'
for f in "$CLI_DIR"/cmd_*.go; do
  case "$f" in *_test.go) continue ;; esac
  base="$(basename "$f")"

  # Form A: flag.FlagSet declarations — fs.Bool("name", ...), fs.String("name", ...)
  declared="$(grep -nE '\bfs\.(String|Bool|Int|Uint|Int64|Uint64|Float64|Duration)\("' "$f" 2>/dev/null \
    | sed -E 's/^([0-9]+):.*fs\.[A-Za-z0-9]+\("([^"]+)".*/  - `--\2` (FlagSet) — line \1/')"

  # Form B: hand-rolled literal matching — "--name"
  literal="$(grep -noE '"--[a-z0-9][a-z0-9-]*"' "$f" 2>/dev/null \
    | sed -E 's/^([0-9]+):"(--[a-z0-9-]+)"/  - `\2` (literal) — line \1/' \
    | sort -u -t'`' -k2,2)"

  if [ -n "$declared" ] || [ -n "$literal" ]; then
    printf '\n### %s\n\n' "$base"
    [ -n "$declared" ] && printf '%s\n' "$declared"
    [ -n "$literal" ] && printf '%s\n' "$literal"
  fi
done

printf '\n## Positional arguments\n\n'
printf 'Taken from the `usage:` strings the commands emit. Positionals are surface too:\n'
printf '`harvest <motive-id> ...` carries the motive noun as a positional, where a\n'
printf 'flag-only scan would report the command as motive-free.\n\n'
grep -rnoE 'usage: [a-z-]+ [^"]*' "$CLI_DIR"/cmd_*.go 2>/dev/null \
  | grep -v '_test.go' \
  | sed -E 's/^([^:]+):([0-9]+):usage: (.*)$/- `\3` — \1:\2/' \
  | sort -u

printf '\n## Subcommand groups\n\n'
printf 'Commands that dispatch to a second level, from their own usage strings.\n\n'
grep -rnE 'missing subcommand|valid:' "$CLI_DIR"/cmd_*.go 2>/dev/null \
  | grep -v '_test.go' \
  | sed -E 's/^([^:]+):([0-9]+):.*/- \1:\2/' \
  | sort -u

printf '\n## Retracted and migrating surfaces\n\n'
printf 'Surfaces a decision removed or is moving away from. Checked against source so\n'
printf 'this section reports what IS, not what was intended.\n\n'
# NOTE: this section hardcodes two in-flight decisions (D-PD-30, D-PD-21) and is
# DELIBERATELY TEMPORARY. Delete it once both retractions are fully landed —
# it is a migration check, not a permanent feature of the extractor.

if grep -rqn 'exec_batch' --include='*.go' . 2>/dev/null; then
  printf -- '- **exec_batch** (retracted by D-PD-30): STILL PRESENT in source — retraction incomplete.\n'
else
  printf -- '- **exec_batch** (retracted by D-PD-30): absent from source. Retraction held.\n'
fi

motive_cli="$(grep -rn 'motive-id' "$CLI_DIR"/cmd_*.go 2>/dev/null | grep -v '_test.go' \
  | sed -E 's/^([^:]+):([0-9]+):.*/\1:\2/' | sort -u | tr '\n' ' ')"
if [ -n "$motive_cli" ]; then
  printf -- '- **motive noun on the CLI** (D-PD-21 replaces it with `--label motive=<id>`): STILL LIVE at %s\n' "$motive_cli"
else
  printf -- '- **motive noun on the CLI** (D-PD-21): gone from the CLI.\n'
fi

motive_api="$(grep -rn 'func .*\(GetByMotive\|HarvestMotive\)' --include='*.go' internal/ 2>/dev/null \
  | grep -v '_test.go' | sed -E 's/^([^:]+):([0-9]+):.*/\1:\2/' | sort -u | tr '\n' ' ')"
[ -n "$motive_api" ] && printf -- '  - core API still exports motive-keyed entry points at %s\n' "$motive_api"

if grep -rqn 'MotiveID' internal/core/store/filestore.go 2>/dev/null; then
  printf -- '  - store: `MotiveID` is legacy read-only, migrated to `Labels["motive"]` on load (filestore.go:32,50,107-114). New records never write it — this half of D-PD-21 landed.\n'
fi

printf '\n## MCP tool kinds\n\n'
printf 'Declared in the MCP server, not the CLI package.\n\n'
grep -rnoE '"(sandbox|snapshot|image|exec|run)_[a-z_]+"' internal/mcp/*.go 2>/dev/null \
  | grep -v '_test.go' \
  | sed -E 's/^([^:]+):([0-9]+):"([^"]+)"/- `\3` — \1:\2/' \
  | sort -u -t'`' -k2,2
