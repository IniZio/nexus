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
- Credentials: **brokered only**. Real tokens never enter the sprite (env, argv, files,
  `/proc`). There is no env-projection tier and no fallback to one.
  - The sprite sees placeholders: `CLAUDE_CODE_OAUTH_TOKEN` (agent `claude-code`) and
    `GH_TOKEN` (push mode or `--secret GH_TOKEN`) hold fixed placeholder strings.
  - A detached host process (`nexus __sprites-broker <id>`) holds one long exec to the
    sprite and runs a relay (`nexus-agent sprite-relay`) on `127.0.0.1:3128`, set as
    `HTTPS_PROXY`. Secret hosts (`api.anthropic.com`, `platform.claude.com`,
    `github.com`, `api.github.com`) are tunnelled over that exec to the host broker,
    which MITMs the request and swaps the placeholder for the real token. Other hosts
    dial direct under the sprite egress policy. Secret hosts are dropped from the
    sprite's direct allowlist.
  - The broker CA is installed into the sprite trust store (needs
    `update-ca-certificates` or `update-ca-trust`, run via `sudo -n` when the exec user
    is not root) and `NODE_EXTRA_CA_CERTS`. A guest without either tool fails
    provisioning.
  - Fail closed: if the broker is dead, exec respawns it once (wait <= 10s), else the
    exec is refused. Stdin EOF or a lost tunnel kills the relay; the secret hosts then
    become unreachable, never direct.
  - GitHub binding (D5): `GH_TOKEN` is bound to one repo, `--repo` or else the worktree
    origin. With neither, no `GH_TOKEN` is brokered.
  - `nexus sandbox create` prints `credentials: brokered (placeholders in sprite; real
    tokens stay on host)`.

## Sync modes

`sync` arg on `delegate_worktree_create`:

| Mode | Source of truth | Needs | Teardown |
|---|---|---|---|
| `bundle` (default) | host worktree | no origin, no creds | exports agent commits to the host worktree ff-only; refuses unexported or uncommitted work unless `force` |
| `push` (opt-in) | origin | brokered `GH_TOKEN` (repo-bound); clones origin over HTTPS (ssh origin auto-converted) | requires the task branch pushed |

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
