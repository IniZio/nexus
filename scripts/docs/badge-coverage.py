#!/usr/bin/env python3
"""
badge-coverage.py
=================
Measures which <Badge .../> occurrences in docs/site/**/*.md are
load-bearing — i.e. removing that single badge causes validate-cli-examples.py
to fail.

Approach: copy the docs tree to a temp dir once; for each badge, remove it
from the temp copy, run the validator, restore, repeat.  The real tree is
never mutated — byte-identical before and after, even on error or interrupt.

Reports:
  • total / detected / undetected counts
  • breakdown by badge type (danger / warning / info)
  • per-file table
  • classification of every undetected badge into one of three buckets:
      no-manifest-entry    — section has nexus3 code but no manifest expectation
      redundant-in-section — another badge of same type remains in the section
      prose-only           — section contains no nexus3 invocation at all

Stdlib only (Python ≥ 3.11 for tomllib).
"""
from __future__ import annotations

import json
import os
import re
import sys
import time
import shutil
import tempfile
import subprocess
from pathlib import Path
from typing import NamedTuple

# ── Paths (honour the same env vars as the validator) ────────────────────────
_script_dir = Path(__file__).parent.resolve()
_repo_root = Path(
    os.environ.get("NEXUS3_REPO_ROOT", str(_script_dir / ".." / ".."))
).resolve()

DOCS_DIR = Path(
    os.environ.get("NEXUS3_DOCS_DIR", str(_repo_root / "docs" / "site"))
)
CLI_DIR = Path(
    os.environ.get("NEXUS3_CLI_DIR", str(_repo_root / "internal" / "cli"))
)
SURFACE_MANIFEST = Path(
    os.environ.get("NEXUS3_SURFACE_MANIFEST", str(DOCS_DIR / "cli-surface.toml"))
)
VALIDATOR = _script_dir / "validate-cli-examples.py"

# ── Honesty baseline ──────────────────────────────────────────────────────────
# Minimum total badge count that must be present in docs/site/**/*.md.
# Lower this by the exact number of badges removed whenever an intentional
# removal is committed; the commit message is the explanation.

# ── Regex patterns ────────────────────────────────────────────────────────────
BADGE_RE = re.compile(r'<Badge\s+type="(danger|warning|info)"[^/]*/>')


# ── Data types ────────────────────────────────────────────────────────────────
class Badge(NamedTuple):
    file: Path       # absolute path in the REAL docs tree
    line: int        # 1-indexed line number
    start: int       # character offset (start of match)
    end: int         # character offset (end of match)
    type: str        # danger | warning | info
    text: str        # full match text


# ── Badge enumeration ─────────────────────────────────────────────────────────
def find_all_badges(docs_dir: Path) -> list[Badge]:
    badges: list[Badge] = []
    for md_file in sorted(docs_dir.rglob("*.md")):
        content = md_file.read_text()
        for m in BADGE_RE.finditer(content):
            line_num = content[: m.start()].count("\n") + 1
            badges.append(Badge(
                file=md_file,
                line=line_num,
                start=m.start(),
                end=m.end(),
                type=m.group(1),
                text=m.group(0),
            ))
    return badges


# ── Validator subprocess ──────────────────────────────────────────────────────
def run_validator(tmp_docs: Path) -> bool:
    """Return True if validator exits 0 (clean)."""
    env = {
        **os.environ,
        "NEXUS3_DOCS_DIR": str(tmp_docs),
        "NEXUS3_CLI_DIR": str(CLI_DIR),
        "NEXUS3_SURFACE_MANIFEST": str(tmp_docs / "cli-surface.toml"),
    }
    result = subprocess.run(
        [sys.executable, str(VALIDATOR)],
        env=env,
        capture_output=True,
    )
    return result.returncode == 0


# ── Section helpers (for classification) ─────────────────────────────────────
def _heading_positions(lines: list[str]) -> list[int]:
    return [i for i, ln in enumerate(lines) if re.match(r"^#{1,6} ", ln)]


def section_of_line(lines: list[str], target: int) -> tuple[int, int]:
    """0-indexed (start, end-exclusive) of the heading section containing target."""
    headings = _heading_positions(lines)
    n = len(lines)
    if not headings:
        return 0, n
    # prefix section before first heading
    if target < headings[0]:
        return 0, headings[0]
    for j in range(len(headings) - 1):
        if headings[j] <= target < headings[j + 1]:
            return headings[j], headings[j + 1]
    return headings[-1], n


def section_has_nexus3(lines: list[str], start: int, end: int) -> bool:
    """Does the section [start, end) have a nexus3 invocation inside a code fence?"""
    in_block = False
    for line in lines[start:end]:
        stripped = line.strip()
        if stripped.startswith("```") or stripped.startswith("~~~"):
            in_block = not in_block
        elif in_block and stripped.startswith("nexus3 "):
            return True
    return False


def section_has_other_badge(
    lines: list[str], start: int, end: int, badge_type: str, exclude_line: int
) -> bool:
    """Does the section [start, end) contain another badge of badge_type
    other than the one on exclude_line (0-indexed)?"""
    for i, line in enumerate(lines[start:end], start=start):
        if i == exclude_line:
            continue
        for m in BADGE_RE.finditer(line):
            if m.group(1) == badge_type:
                return True
    return False


def classify(badge: Badge, content: str) -> str:
    """Classify an undetected badge into one of three buckets."""
    lines = content.split("\n")
    zero_line = badge.line - 1  # convert to 0-indexed
    start, end = section_of_line(lines, zero_line)

    if not section_has_nexus3(lines, start, end):
        return "prose-only"
    if section_has_other_badge(lines, start, end, badge.type, zero_line):
        return "redundant-in-section"
    return "no-manifest-entry"


# ── Sweep ─────────────────────────────────────────────────────────────────────
def sweep(badges: list[Badge]) -> tuple[list[Badge], list[Badge]]:
    """
    Remove each badge from a temp copy of the docs tree, run the validator,
    restore, repeat.  Returns (detected, undetected).
    """
    detected: list[Badge] = []
    undetected: list[Badge] = []

    with tempfile.TemporaryDirectory() as tmpdir:
        tmp_docs = Path(tmpdir) / "site"
        shutil.copytree(DOCS_DIR, tmp_docs)

        total = len(badges)
        for i, badge in enumerate(badges):
            rel = badge.file.relative_to(DOCS_DIR)
            tmp_file = tmp_docs / rel

            original = tmp_file.read_bytes()
            # Remove exactly this badge occurrence using character offsets
            text = tmp_file.read_text()
            modified = text[: badge.start] + text[badge.end :]
            tmp_file.write_text(modified)

            try:
                clean = run_validator(tmp_docs)
            finally:
                # Always restore — even on exception or KeyboardInterrupt
                tmp_file.write_bytes(original)

            if not clean:
                detected.append(badge)
            else:
                undetected.append(badge)

            done = i + 1
            if done % 10 == 0 or done == total:
                pct = done * 100 // total
                print(f"  {done}/{total} ({pct}%) ...", end="\r", flush=True)

    print()  # clear progress line
    return detected, undetected


# ── Reporting ─────────────────────────────────────────────────────────────────
def report(badges: list[Badge], detected: list[Badge], undetected: list[Badge], elapsed: float) -> None:
    TYPES = ("danger", "warning", "info")
    SEP = "=" * 64

    print(f"\n{SEP}")
    print("BADGE COVERAGE SWEEP")
    print(SEP)
    print(f"  Total badges : {len(badges)}")
    print(f"  Detected     : {len(detected)}")
    print(f"  Undetected   : {len(undetected)}")
    print(f"  Wall-clock   : {elapsed:.1f}s")

    print(f"\nBy badge type:")
    print(f"  {'type':<10}  {'total':>5}  {'detected':>8}  {'undetected':>10}")
    print(f"  {'-'*10}  {'-'*5}  {'-'*8}  {'-'*10}")
    for btype in TYPES:
        d = sum(1 for b in detected if b.type == btype)
        u = sum(1 for b in undetected if b.type == btype)
        t = d + u
        print(f"  {btype:<10}  {t:>5}  {d:>8}  {u:>10}")

    print(f"\nPer-file breakdown:")
    all_files = sorted({b.file for b in badges})
    col_w = max(len(str(f.relative_to(DOCS_DIR))) for f in all_files) + 2
    print(f"  {'file':<{col_w}}  {'total':>5}  {'det':>5}  {'undet':>5}")
    print(f"  {'-'*col_w}  {'-'*5}  {'-'*5}  {'-'*5}")
    for f in all_files:
        rel = str(f.relative_to(DOCS_DIR))
        d = sum(1 for b in detected if b.file == f)
        u = sum(1 for b in undetected if b.file == f)
        t = d + u
        print(f"  {rel:<{col_w}}  {t:>5}  {d:>5}  {u:>5}")

    # ── Classify undetected ───────────────────────────────────────────────────
    buckets: dict[str, list[Badge]] = {
        "prose-only": [],
        "redundant-in-section": [],
        "no-manifest-entry": [],
    }
    for badge in undetected:
        content = badge.file.read_text()
        bucket = classify(badge, content)
        buckets[bucket].append(badge)

    print(f"\n{SEP}")
    print("UNDETECTED BADGE CLASSIFICATION")
    print(SEP)
    print(f"  {'no-manifest-entry':<22}: {len(buckets['no-manifest-entry']):3d}"
          "  (section has nexus3 code but validator has no expectation)")
    print(f"  {'redundant-in-section':<22}: {len(buckets['redundant-in-section']):3d}"
          "  (another badge of same type remains in same section)")
    print(f"  {'prose-only':<22}: {len(buckets['prose-only']):3d}"
          "  (section has no nexus3 invocation — requires godog coverage)")
    print(f"  {'TOTAL':<22}: {len(undetected):3d}")

    print(f"\nDetected badges (load-bearing):")
    for badge in detected:
        rel = badge.file.relative_to(DOCS_DIR)
        print(f"  {rel}:{badge.line}  [{badge.type}]  {badge.text}")


# ── Entry point ───────────────────────────────────────────────────────────────
def main() -> None:
    if not DOCS_DIR.exists():
        sys.exit(f"DOCS_DIR not found: {DOCS_DIR}")
    if not VALIDATOR.exists():
        sys.exit(f"Validator not found: {VALIDATOR}")

    # ── Pre-check: validate current docs before sweeping ─────────────────────
    # If a load-bearing badge was removed from the docs tree, the validator
    # fails here before we run any sweep iterations.  This is the primary gate
    # for the regression: badge removed → validator fails → badge-coverage exits 1.
    pre = subprocess.run(
        [sys.executable, str(VALIDATOR)],
        capture_output=True, text=True,
    )
    if pre.returncode != 0:
        print("FAIL: validate-cli-examples.py failed on the current docs tree.")
        print("A load-bearing badge may have been removed.  Validator output:")
        print(pre.stdout.strip())
        sys.exit(1)

    badges = find_all_badges(DOCS_DIR)
    total = len(badges)
    print(f"Found {total} badges in {DOCS_DIR}")
    print(f"Running single-badge-removal sweep ({total} iterations) ...")

    t0 = time.monotonic()
    detected, undetected = sweep(badges)
    elapsed = time.monotonic() - t0

    report(badges, detected, undetected, elapsed)

    # ── Honesty gate: per-file badge census ──────────────────────────────────
    # Badges communicate honesty: partial/not-built badges tell readers what
    # isn't built yet, and an UNBADGED claim asserts the thing IS built. Silent
    # badge removal breaks that contract.
    #
    # This replaces a global floor (total >= N). A global floor cannot see a
    # single badge going missing while the total stays above the line — with 89
    # badges against a floor of 85, four could have vanished unnoticed — and it
    # cannot see a badge removed from one page while another page gains one.
    # A per-file census catches both, because the unit of a false claim is a
    # page, not a repo.
    #
    # The census is a FLOOR PER FILE, not an equality: adding a badge is always
    # honest (it marks something as unbuilt) and needs no baseline update.
    # Removing one requires `--update-baseline` and shows up in the diff as a
    # deliberate act with a number attached.
    if not check_badge_census(badges, update=("--update-baseline" in sys.argv)):
        sys.exit(1)


BASELINE_PATH = Path(__file__).with_name("badge-baseline.json")


def check_badge_census(badges: list[Badge], update: bool) -> bool:
    """Compare per-file badge counts against the recorded baseline."""
    counts: dict[str, int] = {}
    for b in badges:
        rel = str(b.file.relative_to(DOCS_DIR))
        counts[rel] = counts.get(rel, 0) + 1

    if update:
        BASELINE_PATH.write_text(json.dumps(dict(sorted(counts.items())), indent=2) + "\n")
        print(f"\nBaseline updated: {len(counts)} file(s), {sum(counts.values())} badge(s).")
        return True

    if not BASELINE_PATH.exists():
        print(f"\nFAIL: no badge baseline at {BASELINE_PATH}."
              f" Run with --update-baseline to create it.")
        return False

    baseline: dict[str, int] = json.loads(BASELINE_PATH.read_text())
    losses = [
        (f, was, counts.get(f, 0))
        for f, was in sorted(baseline.items())
        if counts.get(f, 0) < was
    ]
    if losses:
        print(f"\nFAIL: {len(losses)} page(s) lost badges since the baseline.")
        for f, was, now in losses:
            print(f"  {f}: {was} -> {now}  ({was - now} removed)")
        print("\nAn unbadged claim asserts the thing is built. If these removals"
              " are intentional, re-run with --update-baseline and commit the"
              " baseline change with a reason.")
        return False

    gained = sum(max(0, counts.get(f, 0) - was) for f, was in baseline.items())
    gained += sum(c for f, c in counts.items() if f not in baseline)
    suffix = f"; {gained} added since baseline" if gained else ""
    print(f"\nBadge census OK: no page lost a badge{suffix}.")
    return True


if __name__ == "__main__":
    main()
