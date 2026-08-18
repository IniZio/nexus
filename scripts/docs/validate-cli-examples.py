#!/usr/bin/env python3
"""
validate-cli-examples.py
========================
Validates every `nexus3 ...` invocation found in docs/site/**/*.md code blocks
against the real flag sets parsed from internal/cli/cmd_*.go.

Exit 0  — all invocations are clean.
Exit 1  — one or more violations found.

WHAT IS CHECKED
  Per-verb flag validation: every flag token (--foo) in a docs invocation is
  verified to exist in the parsed flag set for that verb, or to be in the
  explicit target-only allowlist (which additionally requires a badge on the
  page — danger badge for not-built flags, warning-or-danger badge for partial
  flags).

WHAT IS NOT CHECKED
  Positional arity is NOT enforced. The motivating defect (snapshot create
  --tag, which had a spurious flag, not a spurious positional) was a flag
  issue caught by this checker. Positional arity guards in source (e.g.
  "if len(args) != 1") could in principle be parsed and cross-checked, but
  docs examples routinely use placeholder tokens like <sandbox-ref> that
  would require special-casing to distinguish from real positionals. That
  analysis is left for a future extension of this script.

TARGET SPELLINGS (R1)
  The target uses flat lifecycle verbs: create, ps, rm, start, stop, pause,
  resume. Today's source uses `sandbox <verb>`. The validator maps the flat
  target verbs to the `sandbox` source verb for flag lookup.

  `nexus3 up` is REMOVED from the target — any occurrence is a violation.

REMOVED FLAGS (R2)
  Some flags are removed from the target even though they exist in source today.
  Any code-block occurrence is a violation:
    fork --count      (one child per invocation; fan-out is the orchestrator's loop)
    restore --count   (same)
    create --workspace   (superseded by live virtiofs mounts; use -v/--volume)
    create --capture-max (removed alongside --workspace)

OLD SPELLING (R3)
  `nexus3 sandbox <lifecycle-verb>` in a code block is a violation on every page.
  The target spells lifecycle verbs flat: create/ps/rm/start/stop/pause/resume.
  Prose mapping notes are prose, not code-block invocations, so they are not checked.

Allowlisted target-only flags requiring a <Badge type="danger"...> on the page:
  --volume, -v, --shadow, --service  (create target-only, not built)

Allowlisted partial flags requiring a <Badge type="warning"...> or
<Badge type="danger"...> on the page:
  --context  (create target spelling; built today as --file)

Allowlisted target-only verbs (not yet built but badged where used):
  logs, metrics, agent, secret

Allowlisted target-only flags requiring a <Badge type="danger"...> on the page:
  auth login --agent  (provider profile selector; not yet built)
"""

import os
import re
import sys
from collections import defaultdict

REPO_ROOT = os.path.join(os.path.dirname(__file__), "..", "..")
CLI_DIR = os.path.join(REPO_ROOT, "internal", "cli")
DOCS_DIR = os.path.join(REPO_ROOT, "docs", "site")

# ── Target-only surface: flags that exist in the target spec but not in source.
# Any doc page that uses these flags MUST have a <Badge type="danger"...> nearby.
# Entries here suppress flag-unknown errors; the badge check is not suppressed.
# Key is (verb, subverb) — use None for subverb when flag applies to all subverbs.
# NOTE: flat lifecycle verbs (create, ps, rm, ...) use subverb=None.
TARGET_ONLY_FLAGS: dict[tuple[str, str | None], set[str]] = {
    # Flat target verb "create" maps to source "sandbox create"
    ("create", None): {"--volume", "-v", "--shadow", "--service"},
    # Legacy spelling kept for any non-cli pages still using sandbox group
    ("sandbox", "create"): {"--volume", "-v", "--shadow", "--service"},
    # auth login --agent: provider profile selector, not yet built
    ("auth", "login"): {"--agent"},
}

# Partial flags: exist in the target under a different spelling than source.
# Require a <Badge type="warning"...> OR <Badge type="danger"...> on the page.
PARTIAL_FLAGS: dict[tuple[str, str | None], set[str]] = {
    ("create", None): {"--context"},
    ("sandbox", "create"): {"--context"},
}

# Global target-only flags valid on any verb.
GLOBAL_TARGET_ONLY_FLAGS: set[str] = set()

# Verbs that are target-only (not yet built) — skip invocation checks for these.
# "secret" is target-only (cmd_secret.go does not exist).
TARGET_ONLY_VERBS: set[str] = {"logs", "metrics", "agent", "secret"}

# Verbs removed from the target — any occurrence in docs is a violation.
# "shell" is built today but retired in the target; use exec instead.
REMOVED_VERBS: set[str] = {"up", "shell"}

# Flags removed from the target for specific verbs — any occurrence is a violation.
# Keyed by top-level verb. Note: "create" here covers only the flat lifecycle verb;
# "image build --workspace" uses top-level verb "image" and is unaffected.
REMOVED_FLAGS: dict[str, set[str]] = {
    "fork": {"--count"},
    "restore": {"--count"},
    "create": {"--workspace", "--capture-max"},
}

# Flat lifecycle verbs: the target spelling → source verb for flag lookup.
# Source uses `sandbox <verb>`; target uses the flat form.
FLAT_LIFECYCLE_VERBS: dict[str, str] = {
    "create": "sandbox",
    "ps": "sandbox",
    "rm": "sandbox",
    "start": "sandbox",
    "stop": "sandbox",
    "pause": "sandbox",
    "resume": "sandbox",
}


# ── Parse real flags from cmd_*.go ──────────────────────────────────────────

def parse_go_flags(cli_dir: str) -> dict[str, set[str]]:
    """
    Returns verb -> set of real flag names (with leading --).
    Parses fs.String/Bool/Int/Uint/Uint64/Int64/Duration/Float64 FlagSet
    registrations and hand-rolled 'case "--flag":' / 'case arg == "--flag":'
    blocks in cmd_*.go.
    """
    verb_flags: dict[str, set[str]] = defaultdict(set)

    # FlagSet method registrations: fs.String("flagname", ...)
    flagset_re = re.compile(
        r'fs\.(String|Bool|Int|Uint|Uint64|Int64|Duration|Float64)\(\s*"([^"]+)"'
    )
    # Hand-rolled flag cases: case "--flagname": or case arg == "--flagname":
    case_flag_re = re.compile(
        r'case\s+(?:arg\s*==\s*)?"(--[a-zA-Z][a-zA-Z0-9_-]*)":?'
    )
    # Command registration name: Name: "verbname"
    verb_name_re = re.compile(r'Name:\s+"([a-zA-Z][a-zA-Z0-9_-]*)"')

    for fname in sorted(os.listdir(cli_dir)):
        if not (fname.startswith("cmd_") and fname.endswith(".go")):
            continue
        path = os.path.join(cli_dir, fname)
        with open(path) as f:
            src = f.read()

        # Determine which top-level verb this file registers
        verb_match = verb_name_re.search(src)
        if not verb_match:
            continue
        top_verb = verb_match.group(1)

        # FlagSet-registered flags: split on NewFlagSet boundaries so flags
        # are associated with their context (e.g. "image build")
        segments = re.split(r'flag\.NewFlagSet\(', src)
        for seg in segments[1:]:  # skip text before first FlagSet
            ctx_match = re.match(r'"([^"]+)"', seg)
            ctx = ctx_match.group(1) if ctx_match else top_verb

            for m in flagset_re.finditer(seg):
                flag_name = "--" + m.group(2)
                verb_flags[top_verb].add(flag_name)
                parts = ctx.split()
                if len(parts) >= 2:
                    verb_flags[parts[0]].add(flag_name)

        # Hand-rolled flags
        for m in case_flag_re.finditer(src):
            verb_flags[top_verb].add(m.group(1))

    # Global flags (root.go): only --json
    verb_flags["__global__"] = {"--json"}

    return dict(verb_flags)


# ── Extract nexus3 invocations from markdown ─────────────────────────────────

def extract_invocations(md_path: str) -> list[tuple[int, str, bool, bool]]:
    """
    Returns list of (line_number, invocation_string, has_danger_badge, has_warning_badge).
    Only lines inside fenced code blocks (``` or ~~~) starting with 'nexus3 '
    are extracted. Badge presence is a page-level coarse check.
    """
    with open(md_path) as f:
        src = f.read()

    has_danger_badge = bool(re.search(r'<Badge\s+type="danger"', src))
    has_warning_badge = bool(re.search(r'<Badge\s+type="warning"', src))

    results: list[tuple[int, str, bool, bool]] = []
    lines = src.split("\n")

    in_block = False
    for i, line in enumerate(lines):
        stripped = line.strip()
        if stripped.startswith("```") or stripped.startswith("~~~"):
            in_block = not in_block
            continue
        if in_block:
            # Strip trailing inline comment
            code = re.sub(r'\s+#.*$', '', line).strip()
            if code.startswith("nexus3 "):
                results.append((i + 1, code, has_danger_badge, has_warning_badge))

    return results


# ── Parse a nexus3 invocation ────────────────────────────────────────────────

def parse_invocation(inv: str) -> tuple[str, str | None, set[str]] | None:
    """
    Returns (verb, subverb_or_None, flags_set) or None if unparseable.

    Returns None if:
    - The first token is not 'nexus3'
    - There is no verb token after 'nexus3'

    Positionals are not returned (positional arity is not checked — see
    module docstring for rationale).
    """
    tokens = inv.split()
    if not tokens or tokens[0] != "nexus3":
        return None
    tokens = tokens[1:]  # drop 'nexus3'

    if not tokens:
        # 'nexus3' with no verb — skip, nothing to validate
        return None
    verb: str = tokens[0]
    rest = tokens[1:]

    # Detect known subverbs (legacy sandbox group and other noun groups)
    subverb: str | None = None
    if rest and not rest[0].startswith("-"):
        candidate = rest[0]
        subverbs: dict[str, set[str]] = {
            "sandbox": {"create", "list", "rm", "start", "stop", "pause", "resume"},
            "snapshot": {"create", "list", "rm"},
            "image": {"build", "ls", "prune"},
            "auth": {"login", "logout", "status"},
            # "ssh config" is the target spelling for config-ssh (partial).
            # Recognise it as a subverb so the validator does not flag "config"
            # as an unknown flag.  Pages using it must carry a warning badge.
            "ssh": {"config"},
        }
        if verb in subverbs and candidate in subverbs[verb]:
            subverb = candidate
            rest = rest[1:]

    flags: set[str] = set()
    skip_next = False
    for i, tok in enumerate(rest):
        if skip_next:
            skip_next = False
            continue
        if tok == "--":
            break
        if tok.startswith("--") or (tok.startswith("-") and len(tok) == 2):
            flags.add(tok.split("=")[0])
            # If the next token is a flag value (not a flag itself), skip it
            if "=" not in tok and i + 1 < len(rest) and not rest[i + 1].startswith("-"):
                skip_next = True

    return verb, subverb, flags


# ── Main validation ──────────────────────────────────────────────────────────

def check_old_sandbox_spelling(md_path: str, rel: str) -> list[str]:
    """
    Return violations for any code-block line matching
    `nexus3 sandbox <lifecycle-verb>` — the old spelling, replaced by flat verbs.
    Prose mapping/migration notes are prose, not code blocks, so a code-block
    check is sufficient without false-positives.
    """
    lifecycle_verbs = set(FLAT_LIFECYCLE_VERBS.keys())
    violations: list[str] = []
    with open(md_path) as f:
        lines = f.readlines()
    in_block = False
    for i, line in enumerate(lines):
        stripped = line.strip()
        if stripped.startswith("```") or stripped.startswith("~~~"):
            in_block = not in_block
            continue
        if in_block:
            code = re.sub(r'\s+#.*$', '', line).strip()
            m = re.match(r'nexus3\s+sandbox\s+(\S+)', code)
            if m and m.group(1) in lifecycle_verbs:
                violations.append(
                    f"{rel}:{i + 1}: old spelling 'nexus3 sandbox {m.group(1)}'"
                    f" — use flat verb '{m.group(1)}' (target spelling)"
                    f"\n  invocation: {code}"
                )
    return violations


def main() -> None:
    verb_flags = parse_go_flags(CLI_DIR)

    violations: list[str] = []
    checked = 0

    for dirpath, _, filenames in os.walk(DOCS_DIR):
        if ".vitepress" in dirpath:
            continue
        for fname in sorted(filenames):
            if not fname.endswith(".md"):
                continue
            md_path = os.path.join(dirpath, fname)
            rel = os.path.relpath(md_path, REPO_ROOT)

            # Check old sandbox-group spelling in code blocks (all pages)
            violations.extend(check_old_sandbox_spelling(md_path, rel))

            for lineno, inv, has_danger_badge, has_warning_badge in extract_invocations(md_path):
                parsed = parse_invocation(inv)
                if parsed is None:
                    # No verb or unrecognised prefix — skip silently
                    continue
                verb, subverb, flags = parsed

                # Removed verbs must not appear anywhere
                if verb in REMOVED_VERBS:
                    violations.append(
                        f"{rel}:{lineno}: verb '{verb}' has been removed from the target"
                        f" — delete this example\n  invocation: {inv}"
                    )
                    continue

                # Removed flags for specific verbs
                removed_flags_for_verb = REMOVED_FLAGS.get(verb, set())
                for flag in flags:
                    if flag in removed_flags_for_verb:
                        violations.append(
                            f"{rel}:{lineno}: flag {flag!r} for verb '{verb}' has been"
                            f" removed from the target — delete this flag\n  invocation: {inv}"
                        )

                # Target-only verbs: require danger badge, then skip flag checks.
                if verb in TARGET_ONLY_VERBS:
                    if not has_danger_badge:
                        violations.append(
                            f"{rel}:{lineno}: verb '{verb}' is target-only (not built)"
                            f" but page has no <Badge type=\"danger\">"
                            f" — add badge or remove invocation\n  invocation: {inv}"
                        )
                    continue

                # Flat lifecycle verb: resolve flags from source "sandbox" verb
                source_verb = FLAT_LIFECYCLE_VERBS.get(verb, verb)

                # Real flags for this verb (plus global)
                real_flags: set[str] = set(verb_flags.get(source_verb, set()))
                real_flags |= verb_flags.get("__global__", set())

                # Target-only flags (require danger badge): (verb, subverb) and (verb, None)
                target_only: set[str] = (
                    TARGET_ONLY_FLAGS.get((verb, subverb), set())
                    | TARGET_ONLY_FLAGS.get((verb, None), set())
                    | GLOBAL_TARGET_ONLY_FLAGS
                )

                # Partial flags (require warning or danger badge)
                partial: set[str] = (
                    PARTIAL_FLAGS.get((verb, subverb), set())
                    | PARTIAL_FLAGS.get((verb, None), set())
                )

                checked += 1
                for flag in flags:
                    if flag in removed_flags_for_verb:
                        continue  # already reported above
                    if flag in real_flags:
                        continue
                    if flag in target_only:
                        if not has_danger_badge:
                            violations.append(
                                f"{rel}:{lineno}: flag {flag!r} is target-only (not built)"
                                f" but page has no <Badge type=\"danger\">"
                                f" — add badge or remove flag\n  invocation: {inv}"
                            )
                        continue
                    if flag in partial:
                        if not (has_warning_badge or has_danger_badge):
                            violations.append(
                                f"{rel}:{lineno}: flag {flag!r} is a partial target flag"
                                f" but page has no <Badge type=\"warning\"> or"
                                f" <Badge type=\"danger\">"
                                f" — add badge or remove flag\n  invocation: {inv}"
                            )
                        continue
                    violations.append(
                        f"{rel}:{lineno}: unknown flag {flag!r} for verb"
                        f" '{verb}{' ' + subverb if subverb else ''}'"
                        f" (not in source, not in allowlist)\n  invocation: {inv}"
                    )

    if violations:
        print(f"FAIL: {len(violations)} violation(s) found ({checked} invocations checked)\n")
        for v in violations:
            print(f"  {v}\n")
        sys.exit(1)
    else:
        print(f"OK: {checked} nexus3 invocations checked — all clean")


if __name__ == "__main__":
    main()
