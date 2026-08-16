# Parallel development flow

Fan out work across N independent sandboxes, run tasks concurrently, and harvest results back to
the host. This is the primary pattern for parallel agent workflows and multi-branch development.

---

## Overview

The flow has four stages:

```
SEED       →   WORK (parallel)   →   EXTRACT   →   INTEGRATE
create N         exec in each         harvest        git fetch
sandboxes        concurrently         bundles        on host
```

Each sandbox is fully isolated: separate disk, separate network namespace, separate vsock channel.
Work done in one sandbox cannot affect another. The host integrates results after extraction.

---

## Stage 1 — Seed: create N sandboxes

Use `nexus3 up` to create N records in one call with a disk-space preflight:

```
nexus3 up --count 4 --project dev --label motive=pr-42
```

`up` measures `count × per-sandbox-allocated-bytes` against actual free disk before creating
anything, and rejects with `insufficient_disk` if the host cannot accommodate the batch.
**Allocated bytes, not apparent size** — ext4 images are sparse, so apparent size overstates by
orders of magnitude. After preflight passes, records are created in state `created` (no VMs
started yet).

Then boot each sandbox with a workspace capture:

```
for ref in $(nexus3 sandbox list --json | jq -r '.sandboxes[].ref'); do
  nexus3 sandbox start "$ref" \
    --image nexus3-base:20260807 \
    --workspace /path/to/project
done
```

Alternatively, use `sandbox create` directly for each slot when you want full control over per-sandbox flags:

```
nexus3 sandbox create dev/worker-1 \
  --image nexus3-base:20260807 \
  --workspace /path/to/project \
  --label motive=pr-42

nexus3 sandbox create dev/worker-2 \
  --image nexus3-base:20260807 \
  --workspace /path/to/project \
  --label motive=pr-42
```

### Branching inside the guest

nexus3 automatically seeds a deterministic git identity into each sandbox at boot
(`internal/core/service/git_identity.go`):

- **User identity:** `name = nexus3-bot[<last8>]`, `email = nexus3-bot+<last8>@noreply.nexus3`
  where `<last8>` is the last 8 characters of the sandbox ID (the high-entropy portion).
- **Default branch:** `nexus3/<motive>/<last8>` — set via `[init] defaultBranch` in `/root/.gitconfig`.
  This applies to repositories initialised inside the sandbox with `git init`.

For a workspace that was **cloned** into the sandbox (the common case when `--workspace` is used),
the cloned repo is already on the host's HEAD branch. Create the per-sandbox branch explicitly:

```
nexus3 exec dev/worker-1 -- git checkout -b nexus3/pr-42/worker-1
nexus3 exec dev/worker-2 -- git checkout -b nexus3/pr-42/worker-2
```

Use the `nexus3/<motive>/<slot>` prefix so branches match the seeded convention and are
identifiable after extraction. The user identity (name/email) is already configured automatically —
you do not need to run `git config user.name` or `git config user.email` inside the sandbox.

### Fork from a warm base (alternative)

If all sandboxes start from the same prepared state, `fork` is more disk-efficient than
independent creates (though it is not reflink CoW on ext4 — each child is an independent sparse
copy of the parent's allocated blocks):

```
# Prepare a warm base
nexus3 sandbox create dev/base --image nexus3-base:20260807 \
  --workspace /path/to/project
# ... run setup inside base ...

# Fork N children from the running base
nexus3 fork dev/base --count 4
```

See [Surface reference — `fork`](../surface.md#fork--snapshot-fork-into-n-running-children) for
the full flag set. On ext4 hosts each forked child costs approximately the same allocated disk as
an independent create.

---

## Stage 2 — Work: run tasks in parallel

### Fan-out exec across a motive

Batch exec `exec --label` was retracted (2026-08-15, D-PD-30): no reference tool ships fleet
exec, and a bounded-parallelism wrapper is a host-side concern. Fan out with a shell loop over
the label-selected set:

```
for sb in $(nexus3 --json sandbox list --label motive=pr-42 | jq -r '.data.sandboxes[].handle'); do
  nexus3 exec "$sb" -- go test ./... &
  # bound concurrency to what your host memory allows — at 2 concurrent
  # nexus3 VMs this host measured 84% swap pressure
  while [ "$(jobs -r | wc -l)" -ge 2 ]; do wait -n; done
done
wait
```

### Single-sandbox exec

```
nexus3 exec dev/worker-1 -- git log --oneline -5
```

---

## Stage 3 — Extract: harvest results

### Bundling work with git bundle

Inside each sandbox:

```
nexus3 exec dev/worker-1 -- git bundle create /tmp/work.bundle HEAD
```


### Harvesting all bundles in one call

```
nexus3 harvest pr-42 /tmp/work.bundle ./bundles/
```

`harvest` takes three positional arguments: `<motive-id> <guest-src-path> <host-dest-dir>`.
It copies `<guest-src-path>` from every sandbox whose `motive` label equals `<motive-id>` and
places each sandbox's output in `<host-dest-dir>/<sandbox-id>/`.

If any sandbox fails, `harvest` returns `harvest_partial_failure` but still emits per-sandbox
outcomes for the ones that succeeded. **Known limitation:** a stopped sandbox produces a 0-byte
placeholder rather than a meaningful error at the output path.

**Note:** `harvest` does not accept `--label` flags. The first positional argument is always a
plain motive-id string (the value of the `motive` label, not a `KEY=VALUE` selector).

### Integrating on the host

```
for bundle in ./bundles/*/work.bundle; do
  git fetch "$bundle" HEAD:review/$(basename $(dirname "$bundle"))
done
```

Then inspect the branches normally with `git log`, `git diff`, or your code review tool.

---

## Stage 4 — Integrate: push to the remote

**`nexus3 pr` does not exist.** The host-side push is a manual step today:

```
git push origin review/<sandbox-id>:refs/heads/pr/sandbox-work-a
```

Or open a pull request through your Git host's web UI or CLI (`gh pr create`, `git push
--set-upstream`). A `nexus3 pr` command is not planned in the current milestone.

---

## Disk capacity planning

Before starting a multi-sandbox session:

```
df -h ~/.local/state/nexus3/
```

Rules of thumb (measured on a example-app compose monorepo, 2026-08-15):

| Metric | Value |
|---|---|
| Fresh idle sandbox — apparent size | ~4 GiB |
| Fresh idle sandbox — allocated (actual) | ~120 MiB |
| Warm pilot sandbox after build — allocated | ~4.57 GiB |
| Practical concurrent-builds ceiling (swap) | 2 |
| Ceiling for independent creates (44 GiB free) | ~7 |

Stale disks from prior sessions are the largest risk. Reclaim before a multi-sandbox run:

```
nexus3 sandbox list
nexus3 sandbox rm <stale-ref>
```

`nexus3 reap` reports and optionally deletes orphaned host resources (disk files with no
matching record).

---

## Lifecycle checklist

- A `paused` sandbox must be resumed before it can be stopped or removed. The transition
  `paused → stopped` is illegal and returns an error.
- `up` creates records only — no VMs are booted. Boot each record with `sandbox start` or
  `sandbox create --image`.
- `nexus3 run` is the ephemeral variant: it creates, boots, executes, and removes in one
  command. Use it for throwaway one-shot tasks, not for the parallel flow where you need the
  sandbox to persist across multiple `exec` calls.

---

## See also

- [Surface reference — `up`](../surface.md#up--create-n-sandboxes-in-one-call-with-disk-space-preflight)
- [Surface reference — `exec`](../surface.md#exec--run-a-command-in-a-sandbox-via-the-in-guest-agent)
- [Surface reference — `harvest`](../surface.md#harvest--copy-a-guest-path-from-every-sandbox-in-a-motive)
- [Surface reference — `fork`](../surface.md#fork--snapshot-fork-into-n-running-children)
- [Workspace capture](workspace-capture.md)
