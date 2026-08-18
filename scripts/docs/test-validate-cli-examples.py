#!/usr/bin/env python3
"""
test-validate-cli-examples.py
==============================
Mutation tests for all five manifest lists in cli-surface.toml.

Each test:
  1. Creates a minimal fixture environment (docs dir + TOML manifest) in a
     temporary directory.
  2. Runs validate-cli-examples.py against it via env var overrides
     (NEXUS3_DOCS_DIR, NEXUS3_CLI_DIR, NEXUS3_SURFACE_MANIFEST).
  3. Verifies that the "dirty" fixture (missing badge or removed verb/flag
     present) causes FAIL, and the "clean" fixture causes OK.

Tests cover all five manifest lists:
  1. removed_verbs
  2. removed_flags
  3. target_only_verbs
  4. target_only_flags
  5. partial_flags
  Plus two cross-check tests:
  6. manifest → docs orphan (entry with no matching badge in claimed page)
  7. docs → manifest orphan (flag in code block, not in manifest, no real flag)

Exit 0 — all tests passed.
Exit 1 — one or more tests failed.
"""

import os
import sys
import tempfile
import subprocess
import textwrap

VALIDATOR = os.path.join(
    os.path.dirname(os.path.abspath(__file__)), "validate-cli-examples.py"
)


def run_validator(docs_dir: str, cli_dir: str, manifest_path: str) -> tuple[int, str]:
    """Run the validator with custom dirs via env vars. Returns (returncode, combined output)."""
    env = {
        **os.environ,
        "NEXUS3_DOCS_DIR": docs_dir,
        "NEXUS3_CLI_DIR": cli_dir,
        "NEXUS3_SURFACE_MANIFEST": manifest_path,
    }
    result = subprocess.run(
        [sys.executable, VALIDATOR],
        env=env,
        capture_output=True,
        text=True,
    )
    return result.returncode, (result.stdout + result.stderr).strip()


def write_file(path: str, content: str) -> None:
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w") as f:
        f.write(textwrap.dedent(content))


def run_case(
    tmpdir: str,
    manifest_toml: str,
    doc_pages: dict,
    expect_violations: list[str],
) -> list[str]:
    """
    Write manifest + docs and run validator. Returns list of failure descriptions.
    expect_violations: substrings that must appear in the output when it FAILs.
    """
    docs_dir = os.path.join(tmpdir, "docs", "site")
    cli_dir = os.path.join(tmpdir, "cli")
    os.makedirs(docs_dir, exist_ok=True)
    os.makedirs(cli_dir, exist_ok=True)

    manifest_path = os.path.join(docs_dir, "cli-surface.toml")
    with open(manifest_path, "w") as f:
        f.write(manifest_toml)

    for rel, content in doc_pages.items():
        write_file(os.path.join(docs_dir, rel), content)

    rc, out = run_validator(docs_dir, cli_dir, manifest_path)
    failures = []
    if rc == 0:
        failures.append(f"  expected FAIL but got OK\n  output: {out!r}")
    else:
        for expected in expect_violations:
            if expected not in out:
                failures.append(
                    f"  expected {expected!r} in output\n  got: {out!r}"
                )
    return failures


def run_clean(tmpdir: str, doc_pages: dict) -> list[str]:
    """Run validator and expect OK (zero violations). Returns failure descriptions."""
    docs_dir = os.path.join(tmpdir, "docs", "site")
    cli_dir = os.path.join(tmpdir, "cli")
    manifest_path = os.path.join(docs_dir, "cli-surface.toml")

    # overwrite docs pages with clean content
    for rel, content in doc_pages.items():
        write_file(os.path.join(docs_dir, rel), content)

    rc, out = run_validator(docs_dir, cli_dir, manifest_path)
    if rc != 0:
        return [f"  clean fixture expected OK but got FAIL\n  output: {out!r}"]
    return []


def test(name: str, manifest_toml: str,
         dirty_pages: dict, expect_violations: list[str],
         clean_pages: dict | None = None) -> tuple[str, list[str]]:
    """Run one named mutation test. Returns (name, failure_list)."""
    all_failures = []
    with tempfile.TemporaryDirectory() as tmpdir:
        all_failures += run_case(tmpdir, manifest_toml, dirty_pages, expect_violations)
        if not all_failures and clean_pages is not None:
            all_failures += run_clean(tmpdir, clean_pages)
    return name, all_failures


def main() -> None:
    results: list[tuple[str, list[str]]] = []

    # ── 1. removed_verbs ─────────────────────────────────────────────────────
    # 'shell' is in removed_verbs. A code block using it must produce a violation.
    results.append(test(
        name="removed_verbs",
        manifest_toml='removed_verbs = ["shell"]\n',
        dirty_pages={"cli/fixture.md": """\
## nexus3 exec

```bash
nexus3 shell
```
"""},
        expect_violations=["verb 'shell' has been removed from the target"],
        clean_pages={"cli/fixture.md": """\
## nexus3 exec

```bash
nexus3 exec
```
"""},
    ))

    # ── 2. removed_flags ─────────────────────────────────────────────────────
    # '--workspace' is removed for 'create'. A code block using it must fail.
    results.append(test(
        name="removed_flags",
        manifest_toml="""\
removed_verbs = []

[removed_flags]
create = ["--workspace"]
""",
        dirty_pages={"cli/fixture.md": """\
## nexus3 create

```bash
nexus3 create --workspace /path
```
"""},
        expect_violations=["'--workspace'", "removed from the target"],
        clean_pages={"cli/fixture.md": """\
## nexus3 create

No code block with removed flag.
"""},
    ))

    # ── 3. target_only_verbs (no badge in section → violation) ───────────────
    # 'logs' is target-only. A code block without a danger badge in the section
    # must produce a violation. Adding the badge makes it clean.
    results.append(test(
        name="target_only_verbs",
        manifest_toml="""\
removed_verbs = []

[[target_only_verbs]]
verb = "logs"
page = "cli/fixture.md"
""",
        dirty_pages={"cli/fixture.md": """\
## nexus3 logs

No badge here.

```bash
nexus3 logs
```
"""},
        expect_violations=["verb 'logs' is target-only", 'section has no <Badge type="danger">'],
        clean_pages={"cli/fixture.md": """\
## nexus3 logs <Badge type="danger" text="not built" />

```bash
nexus3 logs
```
"""},
    ))

    # ── 4. target_only_flags (no badge in section → violation) ───────────────
    # '--volume' is target-only for 'create'. Without a danger badge in the
    # section containing the code block, the validator must report a violation.
    results.append(test(
        name="target_only_flags",
        manifest_toml="""\
removed_verbs = []

[[target_only_flags]]
key = "create"
page = "cli/fixture.md"
flags = ["--volume"]
""",
        dirty_pages={"cli/fixture.md": """\
## nexus3 create

No badge here.

```bash
nexus3 create --volume /host:/guest
```
"""},
        expect_violations=["'--volume'", 'section has no <Badge type="danger">'],
        clean_pages={"cli/fixture.md": """\
## nexus3 create <Badge type="danger" text="not built" />

```bash
nexus3 create --volume /host:/guest
```
"""},
    ))

    # ── 5. partial_flags (no badge in section → violation) ───────────────────
    # '--context' is partial for 'create'. Without a warning/danger badge in the
    # section, the validator must report a violation.
    results.append(test(
        name="partial_flags",
        manifest_toml="""\
removed_verbs = []

[[partial_flags]]
key = "create"
page = "cli/fixture.md"
flags = ["--context"]
""",
        dirty_pages={"cli/fixture.md": """\
## nexus3 create

No badge here.

```bash
nexus3 create --context /dir
```
"""},
        expect_violations=["'--context'", "partial target flag"],
        clean_pages={"cli/fixture.md": """\
## nexus3 create <Badge type="warning" text="partial" />

```bash
nexus3 create --context /dir
```
"""},
    ))

    # ── 6. manifest → docs orphan ─────────────────────────────────────────────
    # An entry in target_only_verbs pointing to a page that has no danger badge
    # in any section containing the verb name must produce a manifest violation.
    results.append(test(
        name="manifest→docs orphan (verb in manifest, no badge on claimed page)",
        manifest_toml="""\
removed_verbs = []

[[target_only_verbs]]
verb = "totallyfakeverb"
page = "cli/fixture.md"
""",
        dirty_pages={"cli/fixture.md": """\
## Some page

No mention of the fake verb here, and no badge.
"""},
        expect_violations=[
            "target_only_verbs['totallyfakeverb']",
            "no section containing 'totallyfakeverb' has a danger badge",
        ],
    ))

    # ── 7. docs → manifest orphan ─────────────────────────────────────────────
    # A flag used in a code block that is not in the manifest and not in real
    # flags (CLI dir is empty) must be reported as an unknown flag.
    results.append(test(
        name="docs→manifest orphan (flag in code block, not in manifest)",
        manifest_toml="removed_verbs = []\n",
        dirty_pages={"cli/fixture.md": """\
## nexus3 create

```bash
nexus3 create --totally-unknown-flag
```
"""},
        expect_violations=["unknown flag '--totally-unknown-flag'"],
    ))

    # ── Print results ─────────────────────────────────────────────────────────
    print()
    all_pass = True
    for name, failures in results:
        if failures:
            all_pass = False
            print(f"FAIL [{name}]")
            for f in failures:
                print(f)
        else:
            print(f"PASS [{name}]")

    print()
    if all_pass:
        print("All mutation tests PASS — each manifest list changes the verdict.")
        sys.exit(0)
    else:
        print("FAIL: some mutation tests did not change the verdict as expected.")
        sys.exit(1)


if __name__ == "__main__":
    main()
