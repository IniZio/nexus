#!/usr/bin/env bash
# Validates a single commit subject against conventional-commit format.
#
# Usage:
#   check-commit-subject.sh "feat(scope): add something"
#   check-commit-subject.sh "$PR_TITLE"
#
# Allowed types (must stay consistent with .releaserc.json releaseRules):
#   Release-triggering: feat, fix, perf, refactor
#   Non-releasing:      docs, test, chore, ci, build, style
#
# Format: type[(optional-scope)][!]: non-empty description
# Breaking changes are indicated by a trailing ! before the colon.
# The description must not be empty.
set -euo pipefail

SUBJECT="${1:-}"

if [ -z "$SUBJECT" ]; then
  echo "Usage: $0 \"<commit subject>\"" >&2
  exit 2
fi

# Conventional commit pattern:
#   type         — one of the allowed types
#   (scope)      — optional, parenthesised, non-empty
#   !            — optional breaking-change marker
#   :            — literal colon
#   <space>      — required single space
#   description  — at least one non-whitespace character
PATTERN='^(feat|fix|perf|refactor|docs|test|chore|ci|build|style)(\([^)]+\))?!?: .+'

if echo "$SUBJECT" | grep -qE "$PATTERN"; then
  echo "OK: conventional commit — '$SUBJECT'"
  exit 0
else
  echo "FAIL: '$SUBJECT'"
  echo ""
  echo "Commit subject must match conventional-commit format:"
  echo "  type[(scope)][!]: description"
  echo ""
  echo "Allowed types: feat, fix, perf, refactor, docs, test, chore, ci, build, style"
  echo ""
  echo "Examples:"
  echo "  feat(sandbox): add --mount-named flag"
  echo "  fix: handle nil pointer in volume guard"
  echo "  chore!: drop support for Go 1.21"
  exit 1
fi
