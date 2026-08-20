# Using nexus3 from herdr

herdr is the terminal workspace manager nexus3 integrates with. The plugin turns
every sandbox into something you can see and act on without leaving herdr: a
listing overlay, a guest shell in a pane, and lifecycle actions bound to the
focused workspace.

## Install the plugin <Badge type="tip" text="built" />

The plugin lives in this repo and installs from a local path, so it tracks
whatever is checked out:

```sh
herdr plugin install /path/to/nexus3/plugins/herdr
herdr plugin list
```

Installation runs `build.sh`, which writes a shim pointing at the absolute path
of the `nexus3` on your `PATH`. Two probes must pass first — a `context-cwd`
round-trip and an ABI match — so a mismatched binary fails the install rather
than misbehaving later.

::: warning Rebuild the binary, not just the plugin
The shim records an absolute path. If you rebuild nexus3 to a *different*
location, or install the plugin before building, re-run the install so the shim
is rewritten. A stale binary is the most common cause of "herdr shows old
behaviour": the plugin directory is current and the binary it execs is not.
:::

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

Run `nexus3 __herdr-plugin space-list` to see the current bindings.

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
something removed it outside herdr. `nexus3 __herdr-plugin space-list` shows the
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
