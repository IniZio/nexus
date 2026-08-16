# Workspace capture

Workspace capture seeds a sandbox with a snapshot of your host working tree at the moment the
sandbox is created. Unlike `git archive`, the snapshot includes dirty tracked files, untracked
files, and commits that have not been pushed yet.

---

## Why this matters

`git archive HEAD` drops everything that is not committed and clean. If you are mid-feature, the
sandbox sees a state that does not match what you are actually testing. Workspace capture solves
this by reading the live working tree — staging area, dirty files, and all — and writing it into
the sandbox disk before the guest boots.

---

## Basic usage

Add `--workspace <host-path>` to `sandbox create`:

```
nexus3 sandbox create myproject/dev-1 \
  --file /path/to/project \
  --workspace /path/to/project \
  --memory 4096
```

`--file` tells nexus3 to build the guest image from `/path/to/project/.nexus/Containerfile`.
`--workspace` tells nexus3 to capture the host working tree into the guest at boot time.
The two paths are often the same directory but are independent flags — you can build from a
Containerfile in one place and capture a working tree from another.

---

## Size cap

Workspace capture measures available host disk space at run time and sets the cap automatically.
For most working trees this is sufficient. If your working tree is unusually large (monorepos
with many binary assets, for example), you can override the cap:

```
nexus3 sandbox create myproject/dev-1 \
  --file /path/to/project \
  --workspace /path/to/project \
  --capture-max 10GiB
```

Pass `--capture-max 0` to restore automatic cap derivation explicitly.

**Note on sparse images.** Guest disk images are sparse: the apparent size reported by `ls -lh`
or `stat` is a ceiling, not actual allocated bytes. A fresh idle sandbox may show 4 GiB apparent
while consuming 120 MiB on disk. Use `du -sh` to check actual allocated size. The disk preflight
in `nexus3 up` measures allocated bytes, not apparent size.

---

## What is captured

| Item | Captured? |
|---|---|
| Committed files (HEAD) | Yes |
| Dirty tracked files | Yes |
| Untracked files | Yes |
| Unpushed local commits | Yes |
| Files in `.gitignore` matched by `.dockerignore` | Excluded |

The capture respects `.dockerignore` at the workspace root. Add entries there to exclude build
caches, `node_modules`, large binary assets, or any path that would balloon the image.

---

## Extracting work from the sandbox

After in-guest work is done, bring it back to the host.

**Copy a single file:**

```
nexus3 cp myproject/dev-1:/app/out.patch ./out.patch
```

**Bundle a git branch and fetch it on the host:**

Inside the sandbox:

```
nexus3 exec myproject/dev-1 -- \
  git bundle create /tmp/my-branch.bundle HEAD
```

Back on the host:

```
nexus3 cp myproject/dev-1:/tmp/my-branch.bundle ./my-branch.bundle
git fetch ./my-branch.bundle HEAD:pr/sandbox-work
```

For extracting results across multiple sandboxes at once, see [Parallel dev flow](parallel-dev-flow.md)
and the `nexus3 harvest` command.

---

## Lifecycle note

A sandbox can be paused (`nexus3 sandbox pause`) and resumed (`nexus3 sandbox resume`), which
suspends the VM to disk without losing in-guest state. The transition `paused → stopped` is
**illegal** — resume before stopping:

```
nexus3 sandbox resume myproject/dev-1
nexus3 sandbox stop   myproject/dev-1
```

---

## See also

- [Surface reference — `sandbox create`](../surface.md#sandbox--subcommands-for-the-full-sandbox-lifecycle)
- [Surface reference — `cp`](../surface.md#cp--copy-files-between-host-and-guest)
- [Surface reference — `harvest`](../surface.md#harvest--copy-a-guest-path-from-every-sandbox-in-a-motive)
- [Parallel dev flow](parallel-dev-flow.md)
