# Host auth bundle

The **host auth bundle** is an optional **base64-encoded gzip+tar** that the runtime unpacks in the guest so AI-tool configs (opencode, codex, claude, etc.) exist under `$HOME` in the workspace VM. End users normally never construct it by hand: **`nexus create`** builds and sends it automatically from the machine running the CLI.

## Who builds what

| Source | Behavior |
|--------|----------|
| **`nexus create`** | Runs `authbundle.BuildFromHome()` on the **machine running the CLI** and sends the result as `hostAuthBundle` on `workspace.create`. This is the supported user path. |
| **SDK `workspace.create`** | Optional `hostAuthBundle` for automation. If you omit it, **nothing** is sent. The daemon does **not** re-filter custom blobs—parity with the CLI means following the same packing rules below (or calling the same Go helper from your own tooling). |

There is **no** dedicated `nexus` subcommand for dumping bundles: exposing that was noisy for normal users. Advanced CI can import `packages/nexus/pkg/runtime/authbundle` in a small internal binary or build a matching tarball offline using this page.

**RPC validation:** `ResolveFromOptions` accepts the blob only if base64 decodes and decoded size ≤ **4 MiB**.

## Pack rules (`BuildFromHome`)

Roots under `$HOME` (only these trees are walked):

- `.config/opencode/`
- `.config/codex/`
- `.codex/`
- `.config/openai/`
- `.claude/`

**Included files:** regular files only (symlinks skipped). Typical extensions: `.json`, `.yaml`, `.yml`. Under `.claude/`, `CLAUDE.md` / `claude.md` at the tree root is included; **`.claude/projects/**` is excluded**.

**Per-file cap:** **512 KiB**; larger files are skipped.

**Total:** gzip payload before base64 must stay ≤ **4 MiB** or `BuildFromHome` returns empty.

Implementation: `packages/nexus/pkg/runtime/authbundle/bundle.go`.

## CI checklist

1. Set `NEXUS_ENDPOINT` / `NEXUS_TOKEN` (or your auth).
2. Prefer **`nexus create` from a runner that already has the right tool configs** in `$HOME`, or supply `hostAuthBundle` from automation that implements the rules above (e.g. `authbundle.BuildFromHome()` in Go).
3. Size CI timeouts for **cold Firecracker** (first start can take several minutes)—see [Operations](../guides/operations.md).

## Related

- [CLI](cli.md) — `nexus create`, `nexus doctor` and backends
- [SDK](sdk.md) — `WorkspaceCreateSpec`
- [Operations](../guides/operations.md) — doctor latency, backends, paths
- Repository [AGENTS.md](https://github.com/inizio/nexus/blob/main/AGENTS.md) — remote-first constraints
