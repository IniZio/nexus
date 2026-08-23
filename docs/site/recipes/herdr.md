# Using nexus3 from herdr

herdr is the terminal workspace manager nexus3 integrates with. The plugin turns
every sandbox into something you can see and act on without leaving herdr: a
listing overlay, a guest shell in a pane, and lifecycle actions bound to the
focused workspace.

## Install the plugin <Badge type="tip" text="built" />

### One-command install (Linux x86-64)

The plugin is self-bootstrapping. One command downloads, verifies, and installs
everything:

```sh
herdr plugin install IniZio/nexus3/plugins/herdr
```

`build.sh` (the plugin's build hook) reads `plugins/herdr/nexus3-version` for a
pinned release tag, downloads `nexus3-linux-amd64` and `SHA256SUMS` from GitHub
Releases, verifies the checksum, installs the binary to `~/.local/bin/nexus3`,
ABI-probes it, then runs `nexus3 herdr install-default-shell` to hard-link the
guest shell and print the one config line you still need to paste.

**One remaining manual step.** `install-default-shell` prints a line like:

```
[terminal]
default_shell = ~/.local/bin/nexus3-guest-shell
```

Paste it into `~/.config/herdr/config.toml`. herdr's config is user-owned and
is not written automatically.

Confirm the plugin loaded:

```sh
herdr plugin list
```

### Platform matrix

| Platform | Status |
|---|---|
| Linux x86-64 | **supported** — binary downloaded and installed by `build.sh` |
| macOS (any arch) | no released binary — build from source (see below) |
| Linux arm64 | no released binary — build from source (see below) |

For macOS and Linux arm64, `build.sh` exits with a clear message. Build and
wire the shell manually:

```sh
git clone https://github.com/IniZio/nexus3
cd nexus3 && go build -o ~/.local/bin/nexus3 ./cmd/nexus3
nexus3 herdr install-default-shell
# then paste the printed line into ~/.config/herdr/config.toml
```

Then install the plugin pointing at a local clone so the build hook skips the
download:

```sh
NEXUS3_LOCAL=1 herdr plugin install /path/to/nexus3/plugins/herdr
```

### Local-dev path

If `nexus3` is already on `PATH` and you want to skip the download entirely
(e.g. while iterating on the binary itself), set `NEXUS3_LOCAL=1`:

```sh
NEXUS3_LOCAL=1 herdr plugin install /path/to/nexus3/plugins/herdr
```

`build.sh` uses the binary already on `PATH` and runs the same probes without
touching the download path.

::: warning Rebuild the binary, not just the plugin
The shim records the absolute path of the installed binary. If you install a new
binary to a different location, re-run the install so the shim is rewritten. A
stale shim is the most common cause of "herdr shows old behaviour".
:::

## Sandbox lifecycle in herdr <Badge type="tip" text="built" />

### Create: transactional open

When you run `nexus3: create a sandbox` (or open a worktree pane), nexus3
creates three things in sequence: the sandbox, the herdr workspace, and the
binding that links them. If any step fails **before** the binding is written,
the incomplete pieces are rolled back — you do not end up with orphaned
workspaces. A sandbox that already exists and is running is never destroyed by a
failed pane-open.

### Teardown: one idempotent path

Whether teardown is triggered by the `nexus3: remove this sandbox and close the
space` action, `nexus3 herdr remove`, a `nexus3 rm` cascade, or the
real-time pane-close reap described below, it follows the same path: remove the
sandbox, close the herdr workspace, delete the binding. If the workspace close
does not succeed, the binding is retained rather than deleted, so `nexus3 herdr
prune --apply` can retry it on the next pass — the only copy of the record is
never lost.

### Real-time reap on pane close

Closing the last pane of a worktree sandbox tears it down automatically. herdr
sends a trappable `SIGHUP` to the process group; nexus3 catches it and runs the
teardown path above. This is best-effort. If the signal arrives while nexus3 is
in a non-interruptible state, or if herdr is not available, the teardown is
skipped and the binding remains. `nexus3 herdr prune --apply` is the reliable
backstop — run it after a session to confirm nothing leaked.

### `prune --apply`: 4-case reconciler

`nexus3 herdr prune` (dry-run by default) inspects every binding and classifies
it:

| Binding state | Action |
|---|---|
| sandbox gone + workspace gone | delete binding |
| sandbox gone + workspace live | close workspace, delete binding |
| sandbox live + workspace gone | keep binding, clear stale workspace ID |
| both live | noop |

It also sweeps for `nexus3:`-labelled orphan workspaces that have no binding at
all, and closes them.

```sh
nexus3 herdr prune            # dry-run: report what would change
nexus3 herdr prune --apply    # apply: close workspaces and delete stale bindings
```

### Known residue (D-SHL-27)

The **first** worktree you open for a given repo opens a plain host shell — not
a supervised guest pane. Every subsequent worktree for that repo auto-creates a
supervised sandbox as expected. This is a known limitation left in place by
decision D-SHL-27. The first worktree's sandbox, if one is left orphaned, is
reclaimed by:

```sh
nexus3 herdr prune --apply
```

## What the overlay shows <Badge type="tip" text="built" />

Open it with the **`nexus3: list sandboxes`** action. herdr has no built-in
action menu key, so actions are invoked through whatever you have bound to a
palette — with the `jt.command-palette` plugin that is `prefix+p` — or from a
terminal:

```sh
herdr plugin action invoke workspaces --plugin nexus3
```

The overlay lists every sandbox, however it was created:

```
WORKSPACE     STATE    AGENT        MOUNTS              SPACE  ID
demo/api      running  -            /work               bound  sb-06G1…
demo/agent-1  running  claude-code  /work,nm→/work/nm   -      sb-06G1…
```

| Column | What it answers |
|---|---|
| `WORKSPACE` | the `project/name` handle you pass to every other command |
| `STATE` | `running`, `paused`, `stopped`, `created`, `error` |
| `AGENT` | which agent profile it was created for, or `-` for a plain sandbox |
| `MOUNTS` | guest paths of live host mounts, then `volume→path` for named volumes |
| `SPACE` | `bound` if a herdr workspace is attached, `-` if not |
| `ID` | the sandbox ULID, for commands that want an unambiguous ref |

`MOUNTS` is the column that usually decides what to open: a sandbox with no
mounts gives you a guest shell in `/root`, while one with a live mount drops you
straight into the mounted directory.

The same information is available in a terminal with `nexus3 ps`.

## Actions <Badge type="tip" text="built" />

These appear in herdr's action list for the focused workspace:

| Action | Effect |
|---|---|
| `nexus3: list sandboxes` | open the listing overlay described above |
| `nexus3: create a sandbox` | prompt for image, project and name, then create, boot and open a space |
| `nexus3: attach to a workspace` | reattach to an existing guest session |
| `nexus3: create sandbox space (from local Containerfile)` | build, boot, and open a space in one step |
| `nexus3: open guest pane` | another guest shell in the current space |
| `nexus3: pause this sandbox` | pause the bound sandbox — frees CPU, keeps memory state |
| `nexus3: resume this sandbox` | resume it |
| `nexus3: remove this sandbox and close the space` | remove the sandbox, close the workspace, drop the binding |
| `nexus3: workspace logs` | <Badge type="danger" text="not built" /> prints a not-implemented notice |
| `nexus3: doctor` | substrate and plugin diagnostics |

Every action resolves the sandbox from the focused herdr workspace, so none of
them asks you to type a handle.

## Sandboxes created outside herdr <Badge type="tip" text="built" />

A sandbox made in a terminal is a first-class herdr citizen:

```sh
nexus3 create demo/api --image nexus3-agent-base
```

It appears in the overlay immediately, because the listing is unfiltered. The
first time you run a herdr action against it, nexus3 **adopts** it — creating
the binding that the action needs and telling you so:

```
nexus3: adopted sandbox demo/api into herdr as nexus3:demo/api
```

Adoption is deliberately lazy. Binding at creation time would mint a herdr
workspace for every throwaway sandbox — including the ones `nexus3 run` creates
and deletes seconds later — and would make sandbox creation depend on herdr
being installed. Doing it on first use costs nothing until you actually ask
herdr to act.

Adoption does **not** create a herdr workspace. Pause, resume and remove need
only the sandbox handle; only opening a pane needs a workspace, and that one is
created at the moment it is needed. Otherwise pausing a sandbox would leave an
empty workspace behind every time.

Run `nexus3 herdr list` to see the current bindings.

## A working session

```sh
# create a sandbox with your repo mounted live
nexus3 create demo/api --image nexus3-agent-base --mount "$PWD:/work"

# see it
nexus3 ps
```

Then, in herdr: run the `nexus3: list sandboxes` action, find `demo/api`, and use
`nexus3: open guest pane`. The pane opens a login shell already in `/work`,
because the shell's working directory is derived from the sandbox's first live
mount.

When you are done, `nexus3: remove this sandbox and close the space` tears down
the sandbox, the workspace, and the binding together. Verify nothing leaked:

```sh
nexus3 reap
```

## Troubleshooting

**An action says a sandbox does not exist.** The binding outlived the sandbox —
something removed it outside herdr. `nexus3 herdr list` shows the
stale entry; removing through herdr again clears it.

**The overlay is empty but you know a sandbox exists.** The overlay reads the
same store as `nexus3 ps`. If `ps` also shows nothing, check that both are
running as the same user: state lives under the user's own state directory.

**herdr shows behaviour you already fixed.** Rebuild nexus3 and re-run the
plugin install, per the warning above.

**`stop` says the sandbox is still running.** That is the honest answer, not a
bug in the report: the detached supervisor did not finish within its timeout, so
the record still reads `running`. Wait a moment and check `nexus3 ps`; if the
state does not settle, `nexus3 reap` will show whether anything leaked.
