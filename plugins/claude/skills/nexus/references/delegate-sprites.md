# Delegating to a Sprites sandbox

Same loop as `delegate-loop.md`; this file covers what differs when the sandbox is a
Sprites VM instead of cloud-hypervisor.

## Select the backend

`delegate_worktree_create` takes `backend` (`sprites` | `cloud-hypervisor`).
Precedence: `backend` arg > repo `.nexus/config.yaml` `backend:` > `NEXUS_BACKEND` >
cloud-hypervisor. Records store their backend, so a CH-default MCP server drives
sprites sandboxes (poll, dispatch, teardown) without extra flags.

Prerequisite: a Sprites API token.
- `nexus sprites login < tokenfile` stores `~/.config/nexus/sprites/token` (0600).
- `SPRITES_TOKEN` in the environment wins over the stored file.
- `nexus sprites logout` removes it.

## Trust tier

- Isolation: `guest`. The whole sprite is the boundary; egress policy is the only gate.
- Credentials: tier A, env-projected and **agent-visible**. `CLAUDE_CODE_OAUTH_TOKEN` is
  projected automatically for agent `claude-code`. `GH_TOKEN` is projected only on
  request or in push mode; it is the host token with no TTL.
- Tier B (broker; token never enters the sprite) is not implemented.

## Sync modes

`sync` arg on `delegate_worktree_create`:

| Mode | Source of truth | Needs | Teardown |
|---|---|---|---|
| `bundle` (default) | host worktree | no origin, no creds | exports agent commits to the host worktree ff-only; refuses unexported or uncommitted work unless `force` |
| `push` (opt-in) | origin | `GH_TOKEN`; clones origin over HTTPS (ssh origin auto-converted) | requires the task branch pushed |

## Dispatch and completion

- `delegate_agent_dispatch` takes optional `model` (e.g. `haiku`), passed as `--model`.
- Done marker on sprites: `/home/sprite/.nexus-delegate-done` (not `/run/nexus/delegate-done`).
  The host emits `delegate.done` once.
- Sprite `status` is unreliable for liveness; decide by marker or git, as in `delegate.md`.

## Panes

The sprites pane shell runs through `nexus exec --pty` inside the sprite. Panes never fall
back to a host shell. A pane showing `nexus-guest-shell: REFUSED host shell:` means the
sandbox is unreachable: fix or recreate the sandbox, and send no agent input to that pane.

## Egress

Default deny. Go module and toolchain hosts are allowed; `storage.googleapis.com` only
during the post-seed warm. `--preset docker` opts in to an apt window plus Docker Hub hosts.
Policy authoring: `egress.md`.

## `make` in a sprite

The Makefile detects a sprite guest (root-owned `/.sprite/api.sock`, non-systemd PID 1) and
caps with `GOMEMLIMIT`/`GOMAXPROCS`/`choom`. `NEXUS_ALLOW_UNCAPPED` stays forbidden. See
`self-hosting.md` § make guard in a Sprites guest.

## Known limits

- Seeding a large repo (full-history bundle + `go mod download`) takes ~4 min before the agent starts.
- An open exec session keeps the sprite awake (and billed); close panes at teardown.
- Claude auto-update nags in the pane are harmless.

## Teardown

`delegate_teardown` then `nexus sandbox rm <handle>` deprovisions the sprite. Only
sprites named `nx-*` are ever deleted. Remove the host worktree as in `delegate.md`.
