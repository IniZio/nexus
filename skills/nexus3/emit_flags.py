#!/usr/bin/env python3
"""
emit_flags.py
=============
Reference implementation of the nexus3 skill manifest-probe logic.

Given a project root directory and a slug, prints the --mount-named flag
fragments that the nexus3 skill would emit, one flag per line, ready to
be pasted into a `nexus3 sandbox create` invocation.

Usage:
    python3 emit_flags.py <project-root> <slug>

The output is deterministic: flags are emitted in a fixed order
(npm/pnpm/yarn → rust → go → docker) so tests can assert exact strings.

This file is the testable reference for the prose rules in SKILL.md.
Keeping the two in sync is the responsibility of the maintainer.

Hard rule: any candidate guest path whose components include `.git` (at any
position) is silently dropped. The primitive also enforces this, but the
skill must not emit such paths.
"""

import json
import os
import sys


def _has_git_component(path: str) -> bool:
    """Return True if any path component equals '.git'."""
    parts = path.replace("\\", "/").split("/")
    return ".git" in parts


def emit_flags(project_root: str, slug: str, workspace_prefix: str = "/workspace") -> list[str]:
    """
    Probe *project_root* for known manifests and return a list of
    --mount-named flag strings (without the leading '--mount-named ').

    Each element is of the form:
        <name>:<guest-path>[:<options>]
    """
    flags: list[str] = []
    proj = os.path.basename(os.path.abspath(project_root))
    guest_root = f"{workspace_prefix}/{proj}"

    def flag(name: str, guest_path: str, options: str = "") -> None:
        if _has_git_component(guest_path):
            return
        entry = f"{name}:{guest_path}"
        if options:
            entry += f":{options}"
        flags.append(entry)

    def exists(*parts: str) -> bool:
        return os.path.exists(os.path.join(project_root, *parts))

    def read_json(*parts: str) -> dict:
        try:
            with open(os.path.join(project_root, *parts)) as f:
                return json.load(f)
        except Exception:
            return {}

    def read_text(*parts: str) -> str:
        try:
            with open(os.path.join(project_root, *parts)) as f:
                return f.read()
        except Exception:
            return ""

    # ── Yarn PnP detection (must come before npm/pnpm to suppress node_modules) ─
    pnp_active = False
    if exists(".pnp.cjs"):
        pnp_active = True
    elif exists(".yarnrc.yml"):
        yarnrc = read_text(".yarnrc.yml")
        if "nodeLinker: pnp" in yarnrc:
            pnp_active = True

    if pnp_active:
        flag(f"{slug}-yarn-cache", f"{guest_root}/.yarn/cache", "kind=disk,size=10g")
        flag(f"{slug}-yarn-unplugged", f"{guest_root}/.yarn/unplugged", "kind=disk,size=10g")

    # ── pnpm ────────────────────────────────────────────────────────────────────
    elif exists("pnpm-workspace.yaml"):
        # monorepo: per-package node_modules (emit root-level as representative)
        flag(f"{slug}-node_modules", f"{guest_root}/node_modules", "kind=disk,size=10g")
    elif exists("pnpm-lock.yaml"):
        flag(f"{slug}-node_modules", f"{guest_root}/node_modules", "kind=disk,size=10g")

    # ── npm / Yarn classic ───────────────────────────────────────────────────────
    elif exists("package.json"):
        pkg = read_json("package.json")
        workspaces = pkg.get("workspaces")
        if workspaces:
            # monorepo: one volume per workspace package
            for ws in (workspaces if isinstance(workspaces, list) else workspaces.get("packages", [])):
                pkgname = ws.rstrip("/*").rstrip("/").split("/")[-1]
                flag(
                    f"{slug}-{pkgname}-node_modules",
                    f"{guest_root}/{ws.rstrip('/*')}/node_modules",
                    "kind=disk,size=10g",
                )
        else:
            flag(f"{slug}-node_modules", f"{guest_root}/node_modules", "kind=disk,size=10g")

    # ── Rust ────────────────────────────────────────────────────────────────────
    if exists("Cargo.toml"):
        cargo_text = read_text("Cargo.toml")
        flag(f"{slug}-target", f"{guest_root}/target", "kind=disk,size=15g")
        _ = cargo_text  # workspace vs single-crate: both mount at project root

    # ── Go ──────────────────────────────────────────────────────────────────────
    if exists("go.mod"):
        flag(f"{slug}-go-build", "/root/.cache/go-build", "kind=disk,size=10g")
        flag(f"{slug}-go-mod", "/root/go/pkg/mod", "kind=disk,size=10g")

    # ── Docker ──────────────────────────────────────────────────────────────────
    if exists("Dockerfile") or exists("docker-compose.yml") or exists("compose.yml"):
        flag(f"{slug}-docker", "/var/lib/docker", "kind=disk,size=20g")

    return flags


def main() -> None:
    if len(sys.argv) < 3:
        print(f"Usage: {sys.argv[0]} <project-root> <slug>", file=sys.stderr)
        sys.exit(1)
    project_root = sys.argv[1]
    slug = sys.argv[2]
    flags = emit_flags(project_root, slug)
    for f in flags:
        print(f"--mount-named {f}")


if __name__ == "__main__":
    main()
