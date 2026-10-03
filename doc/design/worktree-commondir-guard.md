# Worktree sandbox: common-dir config/hooks guard

## Problem

Worktree sandboxes mount the host repo's git common dir (`<main>/.git`) read-write at its
host path so commits, refs and objects reach the host. That also exposes `config` and
`hooks/`. A guest agent ran `git config --unset core.hooksPath` and rewrote the host
repo's shared config (delegate-friction-2026-10-02, item 2).

## Mechanism

The host marks the common-dir live mount with the `:gitcommon` option
(`LiveMount.GitCommon` -> `GuestMount.GitCommon` -> `--workspace-mount=...::gitcommon`).
After mounting it rw, nexus-agent bind-mounts `<cd>/config` and `<cd>/hooks` onto
themselves and remounts them read-only (`commondir_guard_linux.go`). objects, refs, logs
and worktrees/<name> stay writable. No extra virtiofsd or virtio-fs device is added.
`extensions.worktreeConfig` is deliberately not enabled: it would itself mutate host config.

### Measured kernel behavior (guest bind over a virtiofs file)

Probe on a live alpine sandbox: bind+remount-ro `config`, write is refused (EROFS). Then
the host rename-replaced `config` (write temp, `mv`). Result: the bind mount silently
disappeared from `/proc/self/mountinfo` (dentry invalidated on FUSE revalidation), and
the guest could then write `config` and rename a lock file over it. So a one-shot bind
over a file is not durable; protection vanishes whenever the host rewrites config.

Hence the agent re-asserts: every 500 ms it reads mountinfo and re-binds any of
`config`/`hooks` that is no longer a read-only mount point. Directory binds (`hooks`) do not
suffer the file-replacement detach but share the same loop. Residual window: up to
500 ms after a host config rewrite.

Rejected: making the whole common dir read-only with rw submounts (breaks
`packed-refs.lock`, `shallow`, and other top-level writes); a second virtiofsd per
sandbox (memory cost).

## Threat model

Defends against accidental or prompt-induced writes by a guest agent running git
commands (`git config`, `make setup` installing hooks). It does not defend against a
root adversary in the guest: root can unmount the overlay, and can still write
`packed-refs`, objects, or other common-dir paths that influence host git behavior.
Treat the common dir as shared state between host and guest.

## Known limits

- Guest `git config` writes to the repo config fail (read-only); per-worktree settings
  must use `-c` or env. Guest hook installs fail on the read-only `hooks/`.
- Agent/host skew: the new 8th `--workspace-mount` field is absorbed as a file name by an
  old agent, so the base image must be rebuilt with the host binary.

## Host-absolute path warning (item 8)

At create time `herdrCommonDirHostPathWarning` scans the common-dir config and prints one
`worktree-sandbox: warning:` line when `core.hooksPath`, `core.excludesFile`,
`core.attributesFile`, `include.path` or `includeIf.*.path` hold an absolute (or `~`)
path outside the common dir and checkout. Those paths exist on the host only.
