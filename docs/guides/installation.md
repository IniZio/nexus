# Installation

**CLI (recommended):**

```bash
curl -fsSL https://raw.githubusercontent.com/inizio/nexus/main/install.sh | bash
nexus --help
```

**Release binaries:** [GitHub releases](https://github.com/IniZio/nexus/releases) — extract and put `nexus` on `PATH`.

**First run:**

```bash
cd /path/to/project
nexus init && nexus create && nexus list && nexus start <workspace-id>
```

Use the id from `nexus create` for `start`, `ssh`, `tunnel`, `stop`, `remove`.

**From source (contributors):** `git clone … && cd nexus && pnpm install && task build`

**Next:** [CLI](../reference/cli.md) · [SDK](../reference/sdk.md) · [Workspace config](../reference/workspace-config.md)