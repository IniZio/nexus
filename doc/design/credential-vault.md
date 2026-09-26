# Credential vault

**Status**: living document — authoritative for `internal/core/vault` and the
`nexus vault` CLI surface introduced in P1.  
See `.groundwork/design/agent-front-door.md` decisions D5, D8–D10 and ledger
decisions D11–D15 for context and rationale.

---

## Problem

Three nexus surfaces need third-party OAuth tokens:

- The **local delegate** (`nexus herdr agent`) authenticates as the laptop user
  to GitHub when it creates worktree branches and opens PRs.
- The **controller** (`cmd/nexus-controller`) authenticates as the Slack user
  who requested the task when it drives the agent and calls Linear or GitHub on
  that user's behalf.
- The **P3 MCP broker** mints short-lived tokens for in-guest MCP shims so
  agents can call Linear, GitHub, and similar APIs without ever holding a raw
  credential.

Storing tokens directly in environment variables or the guest rootfs violates
the zero-cred-in-guest invariant that the cred broker and MITM proxy already
enforce. Keeping three separate token stores would require each consumer to
implement its own refresh, encryption, and project-scope enforcement.

Prior art informing the design:
- **Coder external-auth**: two-level setup (admin registers a provider; each
  user links their own account), keyed `(user, provider)`, with server-side
  refresh. Rejected as a full model because Coder delivers the raw token into the
  workspace via `GIT_ASKPASS`; nexus keeps tokens behind the broker.
- **Gondolin** (earendil-works/gondolin): placeholders in the guest, host proxy
  substitutes the real value per allowed host. Stores nothing, validates the
  zero-cred-in-guest invariant.
- **GitHub Codespaces account secrets**: per-secret repository access list. This
  is the source of the `allowed_projects` field on `Record`.
- **Linear OAuth**: no device flow; authorization code + PKCE;
  `redirect_uri` required.

---

## Principal and integration model

A credential record is identified by a `Key`:

```
Key { Principal, Integration }
```

`Principal` names who is acting:

- `local:<username>` — the OS user on a laptop or self-hosted machine
  (`LocalPrincipal()` in `principal.go`).
- `slack:<team>:<user>` — a Slack user on a shared host controller
  (`SlackPrincipal(team, userID)`); the controller derives this from the
  incoming Slack event. There is no per-user binding table; the principal string
  is deterministic (D15).

`Integration` is the stable connector ID (e.g. `"github"`, `"linear"`).

A `Record` carries the OAuth material for one `Key`:

| Field | Purpose |
|---|---|
| `AccessToken` | Used by the broker and controller when calling the integration |
| `RefreshToken` | Passed to the connector's `Refresh` method |
| `Expiry` | Zero = non-expiring (e.g. GitHub personal access tokens) |
| `Scopes` | Advisory; stored for diagnostic display |
| `AllowedProjects` | Workspace identifiers this record may be used for; `"*"` permits all |

`allowed_projects` defaults to `"*"` on a laptop and to an empty deny-all on a
shared host until the user explicitly sets it (D15). An unset shared-host record
returns `ErrProjectNotAllowed` for every project.

---

## Connector registry

Each OAuth integration is represented by a `Connector`. Two link flows are
supported: `device` (GitHub App, backward-compatible with PATs) and `pkce`
(Linear, which has no device flow).

```
Connector
  ID() string                         // stable identifier: "github", "linear"
  LinkFlow() LinkFlow                 // device | pkce
  StartDevice(ctx) (DeviceAuth, error)
  PollDevice(ctx, deviceCode) (Record, error)
  AuthURL(state) (url, codeVerifier, error)
  Exchange(ctx, code, codeVerifier) (Record, error)
  Refresh(ctx, Record) (Record, error)
  AllowedHosts() []string             // hosts the broker may send this token to
```

Connectors are registered in a `Registry` at process startup. Duplicate IDs
are rejected at registration time (`ErrDuplicateConnector`). P1 ships two
connectors: `github` and `linear`.

**GitHub connector** uses the GitHub App flow (D14): 8-hour access tokens with
refresh, using the nexus-shipped public `client_id`. Hosts may override
`client_id` and `client_secret` in host config to use their own GitHub App.
The real `client_id` to embed in the shipped binary is an open item (see
§Open items).

**Linear connector** uses authorization code + PKCE. The `link` command opens
a browser or prints the URL for the user to visit; there is no device flow.
Linear's OAuth spec requires a fixed `redirect_uri`: on a laptop this is a
loopback listener bound at callback time; on a shared host the controller
registers a fixed localhost callback and the user pastes the resulting redirect
URL into `/link linear <url>` (D11). `tsnet` is not used before P4.

---

## Storage and key layering

`Store` is a low-level key/value interface (`Get`, `Put`, `Delete`, `List`).
V1 implements this as an encrypted file store; tests substitute a `MemStore`.

Encryption uses XChaCha20-Poly1305. The additional-authenticated data (AAD) is
`principal + ":" + integration`, so a ciphertext produced for one key cannot be
replayed under a different key.

The encryption key is loaded through a three-level chain (D9, D12):

1. **systemd `LoadCredentialEncrypted`** — used for the controller process on
   a shared host; the key is bound to the host TPM via systemd's credential
   layer and never appears on disk in plaintext.
2. **OS keyring** — used on a laptop when systemd credentials are unavailable.
3. **0600 key file** — fallback for shared-host service accounts that run
   outside a session with keyring access; the file is owned by the service user.

Shared-host supervisors read the vault key from the 0600 file (not from the
systemd credential layer, which is scoped to the controller process). This is
intentional: supervisors must be able to retrieve tokens at runtime without
escalation.

---

## Refresh policy

The vault's `Source` method returns a `cred.CredentialSource` scoped to a
`(Key, project)` pair. `ErrUnlinked` is returned when no record exists;
`ErrProjectNotAllowed` when the project is not in `AllowedProjects`.

Refresh is **lazy** (D9): triggered on read when `Expiry` is within 5 minutes
of the current time. No background refresher runs. On a 401 response from the
upstream service, the broker retries the credential source once (one refresh
attempt), then fails closed.

---

## Delivery — broker only

Raw tokens **never** leave the vault directly to a guest or to agent environment
variables. Delivery happens through two paths only:

1. **Host broker / MITM** (`internal/core/perimeter/cred`): the cred MITM proxy
   intercepts TLS to hosts in `AllowedHosts` and swaps in the real token after
   the credential source resolves. The guest sees a forwarded connection with no
   credential material.
2. **P3 MCP broker** (future, see §Open items): the controller mints a
   short-lived M2M token from the vault record, adds `acting-user` and
   `project-scope` headers, and forwards to the upstream MCP URL. The in-guest
   shim is credential-free and drives the broker over the authenticated vsock
   channel.

This matches the Gondolin placeholder model: the guest holds no token, only a
connection that the host resolves.

---

## Deployment cases

### Laptop

- Principal: `local:<username>`.
- `allowed_projects` defaults to `"*"`.
- Vault key from OS keyring; 0600 file as fallback.
- GitHub link flow: device flow (print code, user visits URL).
- Linear link flow: PKCE; loopback listener catches the redirect automatically.
- `nexus vault link github` / `nexus vault link linear` are self-service.

### Shared host with controller

- Principals: `slack:<team>:<user>` for each Slack user; `local:<svc-user>` for
  the controller's own GitHub token.
- `allowed_projects` is deny-until-set; users must run `/vault allow <project>`
  or equivalent before the controller can use their token for that workspace.
- Vault key: systemd `LoadCredentialEncrypted` for the controller process;
  0600 key file for supervisors.
- GitHub link flow: device flow (user runs `/link github` in Slack; controller
  posts the device code + URL back; user visits and approves).
- Linear link flow: PKCE with fixed localhost callback; user pastes the
  redirect URL with `/link linear <url>` (D11).
- Unlinked users fail closed (D13): no fallback to a host-global GitHub token.

### P3 MCP broker (future)

The controller's MCP broker resolves credentials from the vault on the
authenticated vsock control channel. The flow is:

1. Guest MCP shim sends a `tools/call` over vsock.
2. Broker resolves `(principal, integration)` from the Biscuit token attached
   to the vsock session.
3. Vault `Source` returns a `CredentialSource`; broker refreshes lazily.
4. Broker adds the token + `acting-user` + `project-scope` to the upstream
   request.
5. `egress.mcp` tool policy is evaluated before the upstream call.

---

## Cutover consequences of D13

There is no fallback to a host-level GitHub token. Every principal — including
`local:<username>` on a laptop — must run `nexus vault link github` before the
delegate or controller can open PRs or call the GitHub API on their behalf.
Running the link command is a one-time step per machine per user.

Unlinked callers receive `ErrUnlinked` immediately; the error is surfaced as a
human-readable prompt to run the link command. No silent degradation.

---

## Open items

- **GitHub App `client_id`**: the connector ships with a placeholder. A real
  GitHub App must be registered and its `client_id` (and, for server-to-server
  refresh, `client_secret` or private key) embedded before P1 ships. Hosts may
  override both values in host config (D14).
- **MCP injection path (D16 — shipped V7)**: host `mcpOAuth` entries are
  imported into the vault on first use via `ImportMCPOAuthIntoVault` (one-shot,
  never overwrites). Integration ID = `"linear"` for `mcp.linear.app` /
  `api.linear.app`; otherwise the server name. `BuildMCPOAuthBindsFromVault`
  resolves bearer tokens via `vault.Source`; an unlinked principal returns
  `ErrUnlinked` immediately (fail closed). The file-based `cred.NewRefresher`
  path in `mcpoauth_refresh.go` is replaced by the indirection var
  `newMCPOAuthRefresher` (defined in `mcpoauth_refresher_ctor.go`) so the
  production file has no direct `cred.NewRefresher` call site.
