# nexus controller

The controller connects a Slack workspace to nexus sandboxes: a Slack mention
starts an agent in a sandbox; replies continue it; reactions track status.

## Prerequisites

- A Slack app with Socket Mode enabled, an app-level token (`xapp-…`), and a
  bot token (`xoxb-…`).
- The `nexus` binary installed (e.g. `~/.local/bin/nexus`).
- A controller config file (see below).

## Config file

```yaml
slack:
  app_token:
    env: SLACK_APP_TOKEN     # xapp- token read from environment
  bot_token:
    env: SLACK_BOT_TOKEN     # xoxb- token read from environment

channels:
  C0123456789:               # Slack channel ID
    repo: /home/user/repos/your-repo   # absolute path to local git checkout
    idle_pause: 10m          # pause sandbox after this idle time
    idle_stop:  1h           # stop sandbox after this idle time

deployment_mode: laptop      # or "shared" for a team host
```

Token values must be `env` (environment variable name) or `file` (path to a
file containing the token). Inline `value:` keys are rejected.

## Running under systemd (Linux)

Generate and install the user unit:

```sh
nexus controller unit systemd > ~/.config/systemd/user/nexus-controller.service
systemctl --user daemon-reload
systemctl --user enable --now nexus-controller
```

The unit sets:

- `PATH` — includes the nexus binary directory and `~/.local/bin`, so
  cloud-hypervisor and other helpers are found even when launched without a
  login shell.
- `TMPDIR=/var/tmp` — image builds write large temporary files; `/dev/shm` is
  limited to 8 GiB by the make cgroup and would be exhausted.
- `LoadCredentialEncrypted=nexus-vault-key` — the vault encryption key is
  delivered via systemd's credential store. Per-workspace supervisors read the
  same key from a 0600 file at the path systemd writes under
  `$CREDENTIALS_DIRECTORY`.

To survive logout, enable linger so the unit stays up without an active session:

```sh
loginctl enable-linger
```

## Running under launchd (macOS)

```sh
nexus controller unit launchd > ~/Library/LaunchAgents/com.nexus.controller.plist
launchctl load ~/Library/LaunchAgents/com.nexus.controller.plist
```

## Credential vault

The vault key is delivered differently by tier:

| Tier | Delivery |
|------|----------|
| Controller (this process) | systemd `LoadCredentialEncrypted` |
| Per-workspace supervisors | 0600 key file read from `$CREDENTIALS_DIRECTORY` |

The split keeps the raw key out of environment variables visible to child
processes, while letting lightweight per-workspace supervisors open the vault
without systemd.
