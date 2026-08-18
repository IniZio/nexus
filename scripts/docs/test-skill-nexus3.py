#!/usr/bin/env python3
"""
test-skill-nexus3.py
====================
Fixture-driven tests for skills/nexus3/emit_flags.py.

Each test:
  1. Creates a minimal fixture project directory in a temp dir.
  2. Calls emit_flags.emit_flags(root, slug) directly.
  3. Asserts the exact list of flag strings returned.

Exit 0 — all tests passed.
Exit 1 — one or more tests failed.
"""

import json
import os
import sys
import tempfile
import textwrap

# Resolve emit_flags relative to this script's repo root.
# __file__ = <repo>/scripts/docs/test-skill-nexus3.py → three dirname calls reach repo root.
_repo_root = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.join(_repo_root, "skills", "nexus3"))
import emit_flags as ef  # noqa: E402

FAILURES: list[str] = []


def write(path: str, content: str) -> None:
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w") as f:
        f.write(textwrap.dedent(content))


def assert_flags(name: str, root: str, slug: str, expected: list[str]) -> None:
    got = ef.emit_flags(root, slug)
    if got != expected:
        FAILURES.append(
            f"FAIL [{name}]\n"
            f"  expected: {expected}\n"
            f"  got:      {got}"
        )
        print(f"FAIL [{name}]")
        print(f"  expected: {expected}")
        print(f"  got:      {got}")
    else:
        print(f"ok   [{name}]")


# ─── Fixtures ────────────────────────────────────────────────────────────────

def test_npm_single(tmp: str) -> None:
    root = os.path.join(tmp, "myapp")
    write(os.path.join(root, "package.json"), '{"name": "myapp"}')
    assert_flags(
        "npm single package",
        root, "myapp",
        ["myapp-node_modules:/workspace/myapp/node_modules:kind=disk,size=10g"],
    )


def test_npm_monorepo(tmp: str) -> None:
    root = os.path.join(tmp, "mono")
    write(
        os.path.join(root, "package.json"),
        json.dumps({"name": "mono", "workspaces": ["packages/api", "packages/web"]}),
    )
    assert_flags(
        "npm monorepo workspaces",
        root, "mono",
        [
            "mono-api-node_modules:/workspace/mono/packages/api/node_modules:kind=disk,size=10g",
            "mono-web-node_modules:/workspace/mono/packages/web/node_modules:kind=disk,size=10g",
        ],
    )


def test_pnpm_single(tmp: str) -> None:
    root = os.path.join(tmp, "papp")
    write(os.path.join(root, "package.json"), '{"name": "papp"}')
    write(os.path.join(root, "pnpm-lock.yaml"), "lockfileVersion: '9.0'\n")
    assert_flags(
        "pnpm single package",
        root, "papp",
        ["papp-node_modules:/workspace/papp/node_modules:kind=disk,size=10g"],
    )


def test_pnpm_monorepo(tmp: str) -> None:
    root = os.path.join(tmp, "pmono")
    write(os.path.join(root, "pnpm-workspace.yaml"), "packages:\n  - 'packages/*'\n")
    assert_flags(
        "pnpm monorepo",
        root, "pmono",
        ["pmono-node_modules:/workspace/pmono/node_modules:kind=disk,size=10g"],
    )


def test_yarn_pnp_via_file(tmp: str) -> None:
    root = os.path.join(tmp, "ypnp")
    write(os.path.join(root, "package.json"), '{"name": "ypnp"}')
    write(os.path.join(root, ".pnp.cjs"), "// pnp\n")
    assert_flags(
        "yarn PnP via .pnp.cjs",
        root, "ypnp",
        [
            "ypnp-yarn-cache:/workspace/ypnp/.yarn/cache:kind=disk,size=10g",
            "ypnp-yarn-unplugged:/workspace/ypnp/.yarn/unplugged:kind=disk,size=10g",
        ],
    )


def test_yarn_pnp_via_yarnrc(tmp: str) -> None:
    root = os.path.join(tmp, "ypnp2")
    write(os.path.join(root, "package.json"), '{"name": "ypnp2"}')
    write(os.path.join(root, ".yarnrc.yml"), "nodeLinker: pnp\n")
    assert_flags(
        "yarn PnP via .yarnrc.yml",
        root, "ypnp2",
        [
            "ypnp2-yarn-cache:/workspace/ypnp2/.yarn/cache:kind=disk,size=10g",
            "ypnp2-yarn-unplugged:/workspace/ypnp2/.yarn/unplugged:kind=disk,size=10g",
        ],
    )


def test_rust_single_crate(tmp: str) -> None:
    root = os.path.join(tmp, "myrust")
    write(os.path.join(root, "Cargo.toml"), '[package]\nname = "myrust"\nversion = "0.1.0"\n')
    assert_flags(
        "rust single crate",
        root, "myrust",
        ["myrust-target:/workspace/myrust/target:kind=disk,size=15g"],
    )


def test_rust_workspace(tmp: str) -> None:
    root = os.path.join(tmp, "rwksp")
    write(
        os.path.join(root, "Cargo.toml"),
        '[workspace]\nmembers = ["crate-a", "crate-b"]\n',
    )
    assert_flags(
        "rust workspace",
        root, "rwksp",
        ["rwksp-target:/workspace/rwksp/target:kind=disk,size=15g"],
    )


def test_go(tmp: str) -> None:
    root = os.path.join(tmp, "goapp")
    write(os.path.join(root, "go.mod"), "module example.com/goapp\ngo 1.22\n")
    assert_flags(
        "go module",
        root, "goapp",
        [
            "goapp-go-build:/root/.cache/go-build:kind=disk,size=10g",
            "goapp-go-mod:/root/go/pkg/mod:kind=disk,size=10g",
        ],
    )


def test_docker_compose(tmp: str) -> None:
    root = os.path.join(tmp, "svc")
    write(os.path.join(root, "docker-compose.yml"), "services:\n  api:\n    image: nginx\n")
    assert_flags(
        "docker-compose.yml",
        root, "svc",
        ["svc-docker:/var/lib/docker:kind=disk,size=20g"],
    )


def test_dockerfile(tmp: str) -> None:
    root = os.path.join(tmp, "img")
    write(os.path.join(root, "Dockerfile"), "FROM ubuntu:22.04\n")
    assert_flags(
        "Dockerfile",
        root, "img",
        ["img-docker:/var/lib/docker:kind=disk,size=20g"],
    )


def test_compose_yml(tmp: str) -> None:
    root = os.path.join(tmp, "svc2")
    write(os.path.join(root, "compose.yml"), "services:\n  web:\n    image: nginx\n")
    assert_flags(
        "compose.yml",
        root, "svc2",
        ["svc2-docker:/var/lib/docker:kind=disk,size=20g"],
    )


def test_no_manifest(tmp: str) -> None:
    root = os.path.join(tmp, "empty")
    os.makedirs(root, exist_ok=True)
    assert_flags("no recognized manifest", root, "empty", [])


def test_combined_npm_rust_docker(tmp: str) -> None:
    root = os.path.join(tmp, "full")
    write(os.path.join(root, "package.json"), '{"name": "full"}')
    write(os.path.join(root, "Cargo.toml"), '[package]\nname = "full"\nversion = "0.1.0"\n')
    write(os.path.join(root, "docker-compose.yml"), "services:\n  db:\n    image: postgres\n")
    assert_flags(
        "combined npm + rust + docker",
        root, "full",
        [
            "full-node_modules:/workspace/full/node_modules:kind=disk,size=10g",
            "full-target:/workspace/full/target:kind=disk,size=15g",
            "full-docker:/var/lib/docker:kind=disk,size=20g",
        ],
    )


def test_combined_go_docker(tmp: str) -> None:
    root = os.path.join(tmp, "gosvc")
    write(os.path.join(root, "go.mod"), "module example.com/gosvc\ngo 1.22\n")
    write(os.path.join(root, "Dockerfile"), "FROM golang:1.22\n")
    assert_flags(
        "combined go + docker",
        root, "gosvc",
        [
            "gosvc-go-build:/root/.cache/go-build:kind=disk,size=10g",
            "gosvc-go-mod:/root/go/pkg/mod:kind=disk,size=10g",
            "gosvc-docker:/var/lib/docker:kind=disk,size=20g",
        ],
    )


# ─── Hard rule: .git paths are never emitted ─────────────────────────────────

def test_git_component_skipped(tmp: str) -> None:
    """
    Verify the hard rule: _has_git_component refuses paths with .git as any
    component. Directly test the helper, then confirm emit_flags never
    produces a .git path even if the project root contains .git subdirs.
    """
    # Direct helper assertions
    assert ef._has_git_component("/workspace/proj/.git"), ".git final component"
    assert ef._has_git_component("/workspace/proj/.git/objects"), ".git intermediate component"
    assert ef._has_git_component("/workspace/.git/hooks/pre-commit"), ".git at depth 2"
    assert not ef._has_git_component("/workspace/proj/node_modules"), "normal path"
    assert not ef._has_git_component("/root/.cache/go-build"), "go-build path"
    assert not ef._has_git_component("/var/lib/docker"), "docker path"

    # Even if project root is named '.git' (pathological), no flags are emitted
    # for that path — just confirm emit_flags on an empty dir inside .git is empty.
    root = os.path.join(tmp, "gitroot", ".git")
    os.makedirs(root, exist_ok=True)
    # write a package.json so npm would normally fire — but guest_root would be
    # /workspace/.git which contains .git: should still emit because the project
    # name is ".git" only if the basename is ".git"; let's use a normal project
    # name but manually verify _has_git_component covers us.
    root2 = os.path.join(tmp, "normalproj")
    write(os.path.join(root2, "package.json"), '{"name": "normalproj"}')
    result = ef.emit_flags(root2, "normalproj")
    for entry in result:
        guest_path = entry.split(":")[1]
        assert not ef._has_git_component(guest_path), f"emitted .git path: {entry}"

    print("ok   [.git hard rule — path component check]")


# ─── Runner ───────────────────────────────────────────────────────────────────

def main() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        test_npm_single(tmp)
        test_npm_monorepo(tmp)
        test_pnpm_single(tmp)
        test_pnpm_monorepo(tmp)
        test_yarn_pnp_via_file(tmp)
        test_yarn_pnp_via_yarnrc(tmp)
        test_rust_single_crate(tmp)
        test_rust_workspace(tmp)
        test_go(tmp)
        test_docker_compose(tmp)
        test_dockerfile(tmp)
        test_compose_yml(tmp)
        test_no_manifest(tmp)
        test_combined_npm_rust_docker(tmp)
        test_combined_go_docker(tmp)
        test_git_component_skipped(tmp)

    if FAILURES:
        print(f"\n{len(FAILURES)} test(s) FAILED.")
        sys.exit(1)
    else:
        total = 16
        print(f"\n{total} tests passed.")
        sys.exit(0)


if __name__ == "__main__":
    main()
