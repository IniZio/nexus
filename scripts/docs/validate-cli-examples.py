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
  explicit target-only allowlist (which additionally requires a badge in the
  same heading-delimited section as the code block — danger badge for
  not-built flags, warning-or-danger badge for partial flags).

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
  Any code-block occurrence is a violation.  See removed_flags in
  docs/site/cli-surface.toml for the current list.

OLD SPELLING (R3)
  `nexus3 sandbox <lifecycle-verb>` in a code block is a violation on every page.
  The target spells lifecycle verbs flat: create/ps/rm/start/stop/pause/resume.
  Prose mapping notes are prose, not code-block invocations, so they are not checked.

SOURCE OF TRUTH FOR ALLOWLISTS
  All five allowlists (target_only_verbs, target_only_flags, partial_flags,
  removed_verbs, removed_flags) are loaded at runtime from:

    docs/site/cli-surface.toml

  Edit that file to add, remove, or reclassify surface.  Every entry in
  target_only_flags and partial_flags MUST have a corresponding <Badge> in the
  doc section noted by the `page` field in cli-surface.toml.
  removed_verbs and removed_flags cannot appear in the docs by definition and
  live exclusively in the manifest.

BADGE GRANULARITY
  Badges are checked at SECTION granularity (heading to next heading), not at
  page level.  A badge in section A does not excuse an unguarded invocation in
  section B of the same page.  This means removing the badge for one specific
  verb or flag is always detected, even when other badges remain on the page.

MANIFEST CROSS-CHECK
  A bidirectional check runs before invocation scanning:
    manifest → docs : every target_only_verbs / target_only_flags / partial_flags
                      entry must resolve to an actual badge in a section of the
                      claimed page that contains the verb or flag name.
    docs → manifest : every target-only or partial flag used in a code block
                      must be listed in the manifest (enforced by the flag-check
                      loop; unknown flags are violations).
  removed_verbs and removed_flags are manifest-only by design (no badge encodes
  a prohibition) and are excluded from the manifest → docs direction.
"""

import os
import re
import sys
import tomllib  # stdlib since Python 3.11 — no third-party deps
from collections import defaultdict

_script_dir = os.path.dirname(os.path.abspath(__file__))
REPO_ROOT = os.environ.get(
    "NEXUS3_REPO_ROOT", os.path.join(_script_dir, "..", "..")
)
CLI_DIR = os.environ.get(
    "NEXUS3_CLI_DIR", os.path.join(REPO_ROOT, "internal", "cli")
)
DOCS_DIR = os.environ.get(
    "NEXUS3_DOCS_DIR", os.path.join(REPO_ROOT, "docs", "site")
)
SURFACE_MANIFEST = os.environ.get(
    "NEXUS3_SURFACE_MANIFEST", os.path.join(DOCS_DIR, "cli-surface.toml")
)


def load_surface_manifest() -> tuple[
    set[str],
    dict[tuple[str, str | None], set[str]],
    dict[tuple[str, str | None], set[str]],
    set[str],
    dict[str, set[str]],
    dict,
]:
    """
    Load the CLI surface manifest from docs/site/cli-surface.toml.

    Returns (target_only_verbs, target_only_flags, partial_flags,
             removed_verbs, removed_flags, manifest_entries).

    manifest_entries holds the raw structured data for the bidirectional
    manifest↔docs cross-check; the first five items are the runtime lookups
    used by the invocation checker.
    """
    try:
        with open(SURFACE_MANIFEST, "rb") as f:
            data = tomllib.load(f)
    except FileNotFoundError:
        print(f"ERROR: surface manifest not found: {SURFACE_MANIFEST}", file=sys.stderr)
        sys.exit(2)

    # target_only_verbs: TOML array-of-tables [{verb, page}, ...]
    tov_entries: list[dict] = data.get("target_only_verbs") or []
    target_only_verbs: set[str] = {e["verb"] for e in tov_entries}

    # target_only_flags: TOML array-of-tables [{key, page, flags}, ...]
    tof_entries: list[dict] = data.get("target_only_flags") or []
    target_only_flags: dict[tuple[str, str | None], set[str]] = {}
    for e in tof_entries:
        key = e["key"]
        flags = set(e.get("flags") or [])
        if "/" in key:
            verb, subverb = key.split("/", 1)
        else:
            verb, subverb = key, None
        target_only_flags[(verb, subverb)] = flags

    # partial_flags: TOML array-of-tables [{key, page, flags}, ...]
    pf_entries: list[dict] = data.get("partial_flags") or []
    partial_flags: dict[tuple[str, str | None], set[str]] = {}
    for e in pf_entries:
        key = e["key"]
        flags = set(e.get("flags") or [])
        if "/" in key:
            verb, subverb = key.split("/", 1)
        else:
            verb, subverb = key, None
        partial_flags[(verb, subverb)] = flags

    # removed_verbs: simple TOML array ["up", "shell"]
    removed_verbs: set[str] = set(data.get("removed_verbs") or [])

    # removed_flags: TOML table {verb: [flags], ...}
    removed_flags: dict[str, set[str]] = {
        verb: set(flags or [])
        for verb, flags in (data.get("removed_flags") or {}).items()
    }

    # Raw structured entries for manifest→docs cross-check
    manifest_entries = {
        "target_only_verbs": tov_entries,
        "target_only_flags": tof_entries,
        "partial_flags": pf_entries,
    }

    return (
        target_only_verbs,
        target_only_flags,
        partial_flags,
        removed_verbs,
        removed_flags,
        manifest_entries,
    )


# Load allowlists from the docs manifest — not hard-coded here.
(
    TARGET_ONLY_VERBS,
    TARGET_ONLY_FLAGS,
    PARTIAL_FLAGS,
    REMOVED_VERBS,
    REMOVED_FLAGS,
    MANIFEST_ENTRIES,
) = load_surface_manifest()

# Global target-only flags valid on any verb (currently empty; extend in manifest
# if a flag needs to be target-only across all verbs).
GLOBAL_TARGET_ONLY_FLAGS: set[str] = set()

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


# ── Section-level badge helpers ───────────────────────────────────────────────

def _split_into_sections(src: str) -> list[tuple[int, int, bool, bool]]:
    """
    Split markdown source into sections bounded by ATX headings.
    Returns a list of (start_line, end_line_exclusive, has_danger, has_warning).

    Section boundaries are heading lines (^#{1,6} ) that appear OUTSIDE fenced
    code blocks.  This means a heading inside a code example is NOT treated as
    a section boundary.

    The first section starts at line 0 and covers any frontmatter / preamble
    before the first heading.
    """
    lines = src.split("\n")
    n = len(lines)

    # Pass 1: find heading line indices outside code blocks
    heading_indices: list[int] = [0]
    in_block = False
    for i, line in enumerate(lines):
        stripped = line.strip()
        if stripped.startswith("```") or stripped.startswith("~~~"):
            in_block = not in_block
        elif not in_block and re.match(r'^#{1,6}\s', line) and i > 0:
            heading_indices.append(i)
    heading_indices.append(n)  # sentinel

    # Pass 2: compute badge presence for each section
    sections: list[tuple[int, int, bool, bool]] = []
    for j in range(len(heading_indices) - 1):
        start = heading_indices[j]
        end = heading_indices[j + 1]
        section_text = "\n".join(lines[start:end])
        has_danger = bool(re.search(r'<Badge\s+type="danger"', section_text))
        has_warning = bool(re.search(r'<Badge\s+type="warning"', section_text))
        sections.append((start, end, has_danger, has_warning))

    return sections


def _page_sections(page_path: str) -> list[str]:
    """
    Return a list of section text strings for the given doc page.
    Used by check_manifest_coverage() to search for verb/flag+badge pairs.
    """
    with open(page_path) as f:
        src = f.read()
    meta = _split_into_sections(src)
    lines = src.split("\n")
    return ["\n".join(lines[s:e]) for s, e, _, _ in meta]


# ── Extract nexus3 invocations from markdown ─────────────────────────────────

def extract_invocations(md_path: str) -> list[tuple[int, str, bool, bool]]:
    """
    Returns list of (line_number, invocation_string, has_danger_badge, has_warning_badge).
    Only lines inside fenced code blocks (``` or ~~~) starting with 'nexus3 '
    are extracted.

    Badge presence is SECTION-LEVEL: the heading-delimited section that contains
    the code block must carry the badge.  A badge in another section of the same
    page does NOT satisfy the requirement for this invocation.  This ensures that
    removing the badge for one specific verb or flag is always detected even when
    other badges remain on the page.
    """
    with open(md_path) as f:
        src = f.read()

    # Build per-line badge lookup from section metadata
    lines = src.split("\n")
    n = len(lines)
    line_danger = [False] * n
    line_warning = [False] * n
    for start, end, has_danger, has_warning in _split_into_sections(src):
        for k in range(start, min(end, n)):
            line_danger[k] = has_danger
            line_warning[k] = has_warning

    results: list[tuple[int, str, bool, bool]] = []
    in_block = False
    i = 0
    while i < n:
        line = lines[i]
        stripped = line.strip()
        if stripped.startswith("```") or stripped.startswith("~~~"):
            in_block = not in_block
            i += 1
            continue
        if in_block:
            # Strip trailing inline comment
            code = re.sub(r'\s+#.*$', '', line).strip()
            if code.startswith("nexus3 "):
                start = i
                # Join backslash continuations into ONE logical invocation.
                #
                # Without this every flag on a continued line is invisible to
                # the checks below, because they only ever saw lines beginning
                # with "nexus3 ". That blind spot let a fabricated --shadow
                # flag (and its --context companion) sit in the docs through
                # repeated green runs; a probe confirmed --totally-bogus-flag
                # on a continuation line also passed clean. 20 of 128 fenced
                # invocations are continued, carrying 61 unchecked flags.
                while code.endswith("\\") and i + 1 < n:
                    i += 1
                    nxt = re.sub(r'\s+#.*$', '', lines[i]).strip()
                    code = code[:-1].rstrip() + " " + nxt
                    if in_block and (lines[i].strip().startswith("```")
                                     or lines[i].strip().startswith("~~~")):
                        # Malformed block (continuation ran past the fence).
                        # Stop joining; the fence toggle is handled next pass.
                        break
                results.append((start + 1, code.strip(), line_danger[start], line_warning[start]))
        i += 1

    return results


# ── Bidirectional manifest↔docs cross-check ───────────────────────────────────

def check_manifest_coverage(docs_dir: str, manifest_entries: dict) -> list[str]:
    """
    Manifest → Docs direction: verify that every entry in target_only_verbs,
    target_only_flags, and partial_flags resolves to an actual badge in a section
    of the claimed page that contains the verb or flag name.

    Docs → Manifest direction: enforced by the invocation checker in main() —
    any flag used in a code block that is not in real_flags and not in the
    manifest is reported as an unknown flag.

    removed_verbs and removed_flags are intentionally excluded from the
    manifest → docs direction: they name surface that CANNOT appear in the
    manual (no badge encodes a prohibition) and legitimately have no doc location.
    """
    violations: list[str] = []

    # ── target_only_verbs ────────────────────────────────────────────────────
    for entry in manifest_entries.get("target_only_verbs") or []:
        verb = entry["verb"]
        page = entry["page"]
        page_path = os.path.join(docs_dir, page)
        if not os.path.exists(page_path):
            violations.append(
                f"manifest: target_only_verbs['{verb}'] references non-existent page {page!r}"
            )
            continue
        found = any(
            verb in section and re.search(r'<Badge\s+type="danger"', section)
            for section in _page_sections(page_path)
        )
        if not found:
            violations.append(
                f"manifest: target_only_verbs['{verb}'] claims <Badge type=\"danger\">"
                f" on {page!r} but no section containing '{verb}' has a danger badge"
                f" — add badge or remove manifest entry"
            )

    # ── target_only_flags ────────────────────────────────────────────────────
    for entry in manifest_entries.get("target_only_flags") or []:
        key = entry["key"]
        page = entry["page"]
        page_path = os.path.join(docs_dir, page)
        if not os.path.exists(page_path):
            violations.append(
                f"manifest: target_only_flags['{key}'] references non-existent page {page!r}"
            )
            continue
        sections = _page_sections(page_path)
        for flag in entry.get("flags") or []:
            found = any(
                flag in section and re.search(r'<Badge\s+type="danger"', section)
                for section in sections
            )
            if not found:
                violations.append(
                    f"manifest: target_only_flags['{key}']['{flag}'] claims"
                    f" <Badge type=\"danger\"> on {page!r}"
                    f" but no section containing '{flag}' has a danger badge"
                    f" — add badge or remove manifest entry"
                )

    # ── partial_flags ────────────────────────────────────────────────────────
    for entry in manifest_entries.get("partial_flags") or []:
        key = entry["key"]
        page = entry["page"]
        page_path = os.path.join(docs_dir, page)
        if not os.path.exists(page_path):
            violations.append(
                f"manifest: partial_flags['{key}'] references non-existent page {page!r}"
            )
            continue
        sections = _page_sections(page_path)
        for flag in entry.get("flags") or []:
            found = any(
                flag in section
                and re.search(r'<Badge\s+type="(warning|danger)"', section)
                for section in sections
            )
            if not found:
                violations.append(
                    f"manifest: partial_flags['{key}']['{flag}'] claims"
                    f" <Badge type=\"warning\"> or <Badge type=\"danger\"> on {page!r}"
                    f" but no section containing '{flag}' has such a badge"
                    f" — add badge or remove manifest entry"
                )

    return violations


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

# Inline-prose exemptions for the old `nexus3 sandbox <verb>` spelling.
#
# The spelling is legitimate in prose in exactly two places: the target ->
# implementation mapping table, and the recurring "current implementation
# uses ..." note that accompanies a partial badge. Both NAME the current
# spelling rather than instructing the reader to use it.
#
# These are deliberately narrow and matched against the surrounding line, not
# against a file or a directory. A blanket per-file skip would recreate the
# blind spot this check exists to close: the two `nexus3 sandbox rm`
# occurrences that survived a green run on 2026-08-19 were on pages that also
# carry legitimate mapping prose.
INLINE_SPELLING_EXEMPTIONS: tuple[tuple[str, str], ...] = (
    ("impl-note", r"current implementation uses"),
    ("mapping-row", r"^\s*\|\s*`nexus3 [^`]+`\s*\|\s*`nexus3 sandbox [^`]+`\s*\|"),
    ("explicit-marker", r"<!--\s*cli-spelling-exempt\s*-->"),
)


def check_inline_cli_spelling(md_path: str, rel: str) -> tuple[list[str], int]:
    """
    Return (violations, exemptions_applied) for inline-backtick `nexus3 ...`
    spans in PROSE that use the old `nexus3 sandbox <lifecycle-verb>` spelling.

    Fenced blocks are handled by check_old_sandbox_spelling; this covers the
    narrative text that check could not see. The exemption count is returned so
    the summary can report it — an exemption list that grows silently is just a
    slower version of the blind spot.
    """
    lifecycle_verbs = set(FLAT_LIFECYCLE_VERBS.keys())
    violations: list[str] = []
    exempted = 0
    with open(md_path) as f:
        lines = f.read().split("\n")
    in_block = False
    for i, line in enumerate(lines):
        stripped = line.strip()
        if stripped.startswith("```") or stripped.startswith("~~~"):
            in_block = not in_block
            continue
        if in_block:
            continue
        for m in re.finditer(r'`(nexus3\s+sandbox\s+[^`]*)`', line):
            span = m.group(1).strip()
            vm = re.match(r'nexus3\s+sandbox\s+(\S+)', span)
            if not vm or vm.group(1) not in lifecycle_verbs:
                continue
            exempt = next(
                (name for name, pat in INLINE_SPELLING_EXEMPTIONS
                 if re.search(pat, line)),
                None,
            )
            if exempt:
                exempted += 1
                continue
            violations.append(
                f"{rel}:{i + 1}: old spelling '{span}' in prose"
                f" — use flat verb '{vm.group(1)}' (target spelling), or if this"
                f" line is naming the current implementation spelling on purpose,"
                f" add <!-- cli-spelling-exempt -->\n  text: {line.strip()}"
            )
    return violations, exempted


def check_old_sandbox_spelling(md_path: str, rel: str) -> list[str]:
    """
    Return violations for any code-block line matching
    `nexus3 sandbox <lifecycle-verb>` — the old spelling, replaced by flat verbs.
    Prose is covered separately by check_inline_cli_spelling.
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
    exempted = 0

    # Bidirectional manifest↔docs cross-check (manifest→docs direction)
    violations.extend(check_manifest_coverage(DOCS_DIR, MANIFEST_ENTRIES))

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

            # ... and in inline-backtick prose, which the above cannot see.
            inline_v, inline_ex = check_inline_cli_spelling(md_path, rel)
            violations.extend(inline_v)
            exempted += inline_ex

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

                # Target-only verbs: require danger badge in the same section, then skip flag checks.
                if verb in TARGET_ONLY_VERBS:
                    if not has_danger_badge:
                        violations.append(
                            f"{rel}:{lineno}: verb '{verb}' is target-only (not built)"
                            f" but section has no <Badge type=\"danger\">"
                            f" — add badge or remove invocation\n  invocation: {inv}"
                        )
                    continue

                # Flat lifecycle verb: resolve flags from source "sandbox" verb
                source_verb = FLAT_LIFECYCLE_VERBS.get(verb, verb)

                # Real flags for this verb (plus global)
                real_flags: set[str] = set(verb_flags.get(source_verb, set()))
                real_flags |= verb_flags.get("__global__", set())

                # Target-only flags (require danger badge in same section): (verb, subverb) and (verb, None)
                target_only: set[str] = (
                    TARGET_ONLY_FLAGS.get((verb, subverb), set())
                    | TARGET_ONLY_FLAGS.get((verb, None), set())
                    | GLOBAL_TARGET_ONLY_FLAGS
                )

                # Partial flags (require warning or danger badge in same section)
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
                                f" but section has no <Badge type=\"danger\">"
                                f" — add badge or remove flag\n  invocation: {inv}"
                            )
                        continue
                    if flag in partial:
                        if not (has_warning_badge or has_danger_badge):
                            violations.append(
                                f"{rel}:{lineno}: flag {flag!r} is a partial target flag"
                                f" but section has no <Badge type=\"warning\"> or"
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
        print(f"FAIL: {len(violations)} violation(s) found ({checked} invocations"
              f" checked, {exempted} inline spelling exemption(s) in force)\n")
        for v in violations:
            print(f"  {v}\n")
        sys.exit(1)
    else:
        print(f"OK: {checked} nexus3 invocations checked — all clean"
              f" ({exempted} inline spelling exemption(s) in force)")


if __name__ == "__main__":
    main()
