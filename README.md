# Nexus

Nexus is a remote workspace daemon and SDK for running, checking, and automating project workspaces across local and VM-backed runtimes.

## Install

Use a published release binary from:

- https://github.com/IniZio/nexus/releases

Pick the archive for your platform:

- `linux-amd64`
- `linux-arm64`
- `darwin-amd64`
- `darwin-arm64`
- `windows-amd64`

Example (macOS arm64):

```bash
tar -xzf nexus-<version>-darwin-arm64.tar.gz
chmod +x nexus-darwin-arm64
mv nexus-darwin-arm64 /usr/local/bin/nexus
```

Verify:

```bash
nexus --help
```

## Quick Start

From your project root:

```bash
nexus init
nexus exec -- echo "hello from nexus"
nexus doctor
```

Common runtime override:

```bash
NEXUS_RUNTIME_BACKEND=firecracker nexus doctor
```

## Docs

- Documentation index: `docs/index.md`
- Workspace daemon package docs: `packages/nexus/README.md`
- JavaScript SDK docs: `packages/sdk/js/README.md`
- CLI and daemon reference: `docs/reference/cli.md`
- SDK reference: `docs/reference/sdk.md`

## Development

Use the repository tasks:

```bash
task ci
task test
task lint
task build
```
