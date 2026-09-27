---
id: C-CRED
type: concept
title: Credential delivery and SSH relay
parent: C-NEXUS
summary: "Requirements for broker/placeholder credential delivery to claude-code sandboxes, auto permission mode, git SSH relay with policy guard, and port auto-forward."
---

# Credential delivery and SSH relay (REQ-CRED-*)

Covers the `nexus-mount-creds-ssh-relay` motive: broker/placeholder/MITM credential delivery to all agent profiles (claude-code, cursor, opencode, oh-my-pi), auto permission mode (no `--dangerously-skip-permissions`), git over SSH via vsock relay with policy guard and branch enforcement, brokered `GH_TOKEN` regression guard, and port auto-forward with herdr ≥ 0.9. The live `~/.claude` virtiofs mount and CredGuardian are retired (D-15, adopt-openshell-lessons, 2026-09-21).

Charter trace prefix: `REQ-CRED-*` maps to spec nodes `CRED-R-001` … `CRED-R-006`.

## Design notes — oh-my-pi profile (`CredentialFormatOhMyPiVault`)

### Auth architecture (omp as of @oh-my-pi/pi-ai 18.2.6)

Oh-my-pi (`omp`) does not read OpenCode's `auth.json` and does not keep a
single JSON bearer file. Live credential resolution order:

1. **Auth-broker daemon** when `OMP_AUTH_BROKER_URL` is set. Bearer is
   `OMP_AUTH_BROKER_TOKEN`, else `auth.broker.token` in
   `<agentDir>/config.yml`, else `~/.omp/auth-broker.token`.
2. **Local SQLite vault** at `<agentDir>/agent.db`, table
   `auth_credentials`. Rows are per provider: `{type:api_key,key}` or
   OAuth `{access,refresh,expires}`. Login via `omp auth-broker login
   <provider>` or the in-TUI `/login` command.
3. **Env-var fallback** per provider (`ANTHROPIC_OAUTH_TOKEN`,
   `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, …). `omp --api-key` overrides
   for one run.

### Why import refuses the vault

A `DedicatedCredStore` holds one access token. Picking a row from the
multi-provider vault requires nexus to guess a provider, which is unsafe.
`ImportOhMyPiCredentials` always returns `ErrOhMyPiVaultNotImportable` and
never modifies the vault.

`CredentialFormatOhMyPiVault` registers a nil `ImportFn`: `CheckCred`
treats nil `ImportFn` as OK so a missing vault does not block sandbox
create; `SourceFn` returns `nil, nil` because the vault is not a single
bearer; `ImportFromPathFn` routes to `importOhMyPiCredentialsAt` so the
explicit-path codepath (tests) refuses cleanly rather than falling through
to `CredentialFormatNone`'s Claude importer.

### Path resolution (`OhMyPiCredPath`)

Matches `@oh-my-pi/pi-utils` `dirs.ts` (18.2.6):

- A named `OMP_PROFILE` / `PI_PROFILE` derives its own agent directory and
  ignores `PI_CODING_AGENT_DIR`; when an override does apply it disables
  the XDG redirect.
- Otherwise the agent directory is `$HOME/<PI_CONFIG_DIR or .omp>/agent`,
  or `$HOME/<config>/profiles/<profile>/agent` when a profile is set
  (`PI_PROFILE` is the fallback only when `OMP_PROFILE` is unset).
- On Linux and Darwin, if that default location is in use and
  `$XDG_DATA_HOME/omp` already exists as a directory, the vault is
  `$XDG_DATA_HOME/omp/agent.db`. XDG is not used merely because the
  variable is set.

`ohMyPiOverrideIsBypassedProfileDir` compares against the raw env value,
not a cleaned path; `dirs.ts readPiProfileFromEnvSafe` ignores an invalid
`PI_PROFILE` rather than erroring.

## Design notes — no-write guarantees

### Oh-my-pi vault (`agent.db`)

`importOhMyPiCredentialsAt` reads at most 16 bytes (the SQLite magic
header) and then refuses. It never writes, renames, truncates, or picks a
provider row. Regression guard:
`TestImportOhMyPiCredentials_FileUnmodifiedAfterImport`
(`internal/core/perimeter/cred/ohmypi_nowrite_test.go`) — compares
`agent.db` mtime + content before and after import; a spurious
`os.WriteFile` inside `importOhMyPiCredentialsAt` changes the mtime and
fails the assertion. Root-safety: mtime + content comparison is
uid-independent; `chmod(0o444)` fixtures are vacuous under root
(`CAP_DAC_OVERRIDE`).

### Codex auth.json

`ImportCodexCredentials` reads `auth.json` to classify the format and then
refuses (refresh tokens rotate on use; a write would corrupt the live
login). Regression guard:
`TestImportCodexCredentials_DoesNotTouchAuthFile`
(`internal/core/perimeter/cred/codex_nowrite_test.go`) — verifies mtime,
content, and directory entry count are all unchanged after import, even
when `CODEX_HOME` is pointed at a temp directory.
