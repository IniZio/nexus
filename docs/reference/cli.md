# CLI Reference

Direct control for creating, starting, and accessing isolated workspaces.

## Install and first run

```bash
curl -fsSL https://raw.githubusercontent.com/inizio/nexus/main/install.sh | bash
cd /path/to/project
nexus init && nexus create && nexus list && nexus start <workspace-id>
```

`nexus create` prints the workspace id used by `start`, `ssh`, `tunnel`, `stop`, `remove`.

**Create and host auth bundle:** `nexus create` runs `authbundle.BuildFromHome()` on **the machine running the CLI**, then sends it as `hostAuthBundle`. End users do not invoke a separate bundle command. SDK `workspace.create` without `hostAuthBundle` sends no tarball; advanced packing rules are in [`host-auth-bundle.md`](host-auth-bundle.md) (see also [`sdk.md`](sdk.md)).

## Common commands

```bash
nexus init [project-root] [--force]
nexus create [--backend firecracker]
nexus list
nexus start|stop|remove|ssh|tunnel <workspace-id>
nexus fork --id <workspace-id> --name <child-name> [--ref <child-ref>]
nexus fork <id> <name> [--ref <ref>]
nexus exec [path] [--timeout 10m] -- <command> [args...]
nexus doctor [--report-json path]
```

- **`nexus ssh`:** optional `--shell`, `--command` (non-interactive one shot).
- **`nexus tunnel`:** applies compose port forwards; blocks until Ctrl-C.
- **`nexus init`:** default path is cwd; `--force` overwrites `.nexus` scaffold. Host setup may escalate privileges (`sudo`); use `sudo -E nexus init --force` only where non-interactive sudo is unavailable.
- **`nexus exec`:** default path is cwd; pass an explicit path as first argument to target another directory.

## `nexus doctor` and backends

`nexus doctor` runs from the current directory (or the path passed to `nexus exec`). There is no top-level `--timeout`; individual probes use their own timeouts.

On a **cold Firecracker** workspace, the first run can take **several minutes** while the guest and tooling bootstrap—silence on the terminal can mean runtime setup, not only your `.nexus/probe` scripts. **Seatbelt** (often selected on macOS when nested virtualization is unavailable) is usually much faster. Backend selection follows `nexus create` / host capabilities; see [`workspace-config.md`](workspace-config.md) for runtime notes.

## Related

- SDK: [`sdk.md`](sdk.md)
- Host auth bundle: [`host-auth-bundle.md`](host-auth-bundle.md)
- Workspace config: [`workspace-config.md`](workspace-config.md)

