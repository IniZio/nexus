# Dev-environment egress policy for agent sandboxes

## `--allow-host` for agent sandboxes

An agent sandbox (`--agent <name>`) boots with **default-deny egress**. Only the
agent profile's own hosts are reachable — for `claude-code` that is
`api.anthropic.com` and `platform.claude.com`. Nothing else resolves, so every
package manager fails at connect time until its hosts are named explicitly.

Add dev-toolchain hosts with `--allow-host <host>` (repeatable):

```sh
nexus3 create <project>/<name> --image <digest> --agent claude-code \
  --mount /path/to/checkout:/work \
  --allow-host proxy.golang.org \
  --allow-host sum.golang.org \
  --allow-host storage.googleapis.com
```

## Do not combine `--allow-host` with `--egress closed` on an agent sandbox

The flag's own help text says `--allow-host` applies "when `--egress closed`".
For an agent sandbox that combination is **structurally impossible**:
`--egress closed` requires `--repo`, which binds a GitHub secret, which
`service.ValidateSecrets` refuses with `ErrAgentGitHubSecret`.

`--allow-host` works on an agent sandbox **without** `--egress closed` —
`resolveAgentPosture` unions the list onto the profile's hosts unconditionally.
Pass `--allow-host` alone.

## Per-ecosystem host sets

| Toolchain | Hosts required |
|-----------|----------------|
| Go | `proxy.golang.org`, `sum.golang.org`, `storage.googleapis.com` |

`storage.googleapis.com` is **not optional** for Go: `proxy.golang.org` serves
module zips as redirects to signed `storage.googleapis.com` URLs. Omitting it
produces a build that downloads part of the dependency graph and then fails with
`connection refused` — a confusing partial failure, not a clean one.

Note that `storage.googleapis.com` is a broad host (every GCS bucket). Treat
adding it as a deliberate widening, not a formality.

Host sets for other ecosystems are **not yet verified**. Determine them by
running the toolchain and reading the refused hostnames out of the failure —
do not guess.

## Authoring nexus3.yaml egress policy

For authoring `nexus3.yaml` egress policy, secret brokering, and verification
probes, see `nexus3:nexus3-egress`.
