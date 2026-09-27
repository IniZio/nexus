# Supervisor own-scope: cgroup independence from spawner

## Problem

The per-sandbox detached supervisor (`nexus __supervisor`) and its child
processes (cloud-hypervisor, virtiofsd) run in the cgroup of the unit that
spawned them. When that unit is restarted (e.g. `nexus-controller@dogfood`
with `KillMode=control-group`), every process in the cgroup is killed,
including live VMs. The same hazard applies to `make test` systemd-run scopes
and any other transient spawner unit.

The supervisors also inherit the controller's `OOMScoreAdjust=500`, making
VMs preferred OOM victims.

## Fix

When a systemd user manager is reachable (detected by
`$XDG_RUNTIME_DIR/systemd/private` existing), `SpawnDetached` launches the
supervisor via `systemd-run --user --scope --collect --unit=nexus-sb-<ref>`.
The scope unit is registered with the user manager and its cgroup is
independent of the calling process's unit.

`OOMScoreAdjust=0` is set on the scope so the supervisor and its VM children
are not preferentially killed under memory pressure.

For ephemeral supervisors (builder) and spawns that pass file descriptors
(watchdog pipe, cache-disk lease FDs), the direct-fork path is kept because
`systemd-run` cannot forward arbitrary FDs through a scope.

## Naming and collision handling

Unit name: `nexus-sb-<sandboxRef>` with non-identifier characters replaced by
`_`. A `systemctl --user reset-failed <unit>.scope` is issued before each
spawn to clear any leftover failed-state unit with the same name.

## Fallback

If the systemd user manager is not reachable (no `XDG_RUNTIME_DIR`, or the
`systemd/private` socket is absent), `SpawnDetached` falls back to the
existing `Setsid`-based direct fork. The supervisor still detaches from the
calling session but remains in the spawner's cgroup.
