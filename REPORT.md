# opencode agent profile

## What changed

Added a static-key profile for the OpenCode CLI (`opencode-ai` on npm), following the cursor importer rather than the Claude Code OAuth refresher.

- `internal/core/perimeter/cred/opencode.go` — reads `opencode-go` from `auth.json`. Accepts only `{type:"api", key}`. Missing file, absent provider, empty key, and any other grant (`oauth`, `wellknown`) are errors. No refresher, no JWT expiry parse. Registered on `CredentialFormatOpencodeAPIKey` from `init` so `source.go` stays untouched.
- `internal/core/perimeter/cred/profile.go` — `OpencodeProfileName = "opencode"` and `OpencodeProfile`, added to `profiles`. Recipe is a pinned Node 22.23.2 tarball (npm is not in the guest base image) plus `opencode-ai@1.18.31` (`RecipeKindNPM`). `PlaceholderIsJWT` is false.
- `opencode_test.go` / `opencode_nowrite_test.go` — import, path, registry, pin, and no-write coverage.

`cursor.go` and `claudecode.go` were not edited.

## Auth shape (upstream v1.18.31)

Verified against the tagged source and the live models catalog, not the prior comment.

- File: `$XDG_DATA_HOME/opencode/auth.json`, default `~/.local/share/opencode/auth.json` (`packages/core/src/global.ts` via `xdg-basedir`, `packages/opencode/src/auth/index.ts`).
- `opencode-go` value is `{type:"api", key:string}`. `oauth` is `{type, refresh, access, expires}`. `wellknown` is a third grant. Those are refused.
- models.dev `api.json` (fetched 2026-09-19):
  - `opencode-go`: `env: ["OPENCODE_API_KEY"]`, `api: https://opencode.ai/zen/go/v1`
  - `opencode` (OpenCode Zen): same env var, `api: https://opencode.ai/zen/v1`
- Both API hostnames are `opencode.ai`. `EgressHosts` is `["opencode.ai"]`. `api.opencode.ai` is the wrong base URL (upstream issue #27853).
- The key is passed through as `@ai-sdk/openai-compatible` `apiKey`. The CLI does not JWT-parse it. A hex placeholder is enough. `PlaceholderIsJWT` stays false.
- Catalog fetch host is `https://models.opencode.ai` (`packages/core/src/models-dev.ts`), not a credential host. `OPENCODE_DISABLE_MODELS_FETCH` and `OPENCODE_DISABLE_AUTOUPDATE` are real `flag.ts` `truthy()` flags (`"1"` / `"true"`).

The guest seed writer can only emit a flat JSON object (`{"key":"<placeholder>"}`). OpenCode ignores that shape (`Auth.all` drops entries that fail the schema). The working guest channel is `OPENCODE_API_KEY`, which `provider.ts` loads from `provider.env` before auth.json. The host importer still reads the nested file.

## Mutation

RED: `importOpencodeCredentialsAt` was changed to accept `type:"oauth"` as well as `"api"`. The oauth fixture carries a non-empty `key`, so the empty-key check cannot hide the miss.

```
--- FAIL: TestImportOpencodeCredentials_WrongType (0.00s)
    opencode_test.go:107: expected error for oauth entry; got nil
FAIL
FAIL	github.com/IniZio/nexus/internal/core/perimeter/cred	0.007s
```

The type check was restored. GREEN:

```
ok  	github.com/IniZio/nexus/internal/core/perimeter/cred	1.146s
```

`make vet` (`go vet -p 4 ./...`) also passed. This guest's pid 1 is `nexus-agent`, so `systemd-run` does not exist. Both runs used the Makefile's `NEXUS_ALLOW_UNCAPPED=1` path (choom only), narrowed to `./internal/core/perimeter/cred/`.

## Live verification

The operator file `~/.local/share/opencode/auth.json` is not present in this guest (host home is not mounted). The first `nexus create` built the image, then preflight refused, which is the correct absent-credential result:

```
error: sandbox create: opencode: credential not found; run 'nexus auth login --agent opencode' to provision it
```

The image cache then hit (`build-cache: hit — skipping builder VM`). A second create used a synthetic `opencode-go` api key under a temp `XDG_DATA_HOME` only so preflight could pass. No second builder VM. Sandbox memory was capped (`--memory 512 --memory-max 1024 --builder-memory 1024`). Boot:

```
created sandbox test/opencode-auth-check (sb-06GBHJMV5HT71D6H8Y40YDKSB4)
```

Guest checks:

- `opencode --version` → `1.18.31` (`/usr/local/bin/opencode` → `opencode-ai/bin/opencode.exe`). `node` is `/usr/local/bin/node`.
- `~/.local/share/opencode/auth.json` is absent. The only `auth.json` is the seeded placeholder at `/run/nexus/cred-dir/opencode/auth.json`: `{"key":"<64-hex>"}`. The synthetic key is not in `cred.env` or that file.
- `cred.env` contains `OPENCODE_API_KEY=<same hex placeholder>`, `OPENCODE_DISABLE_AUTOUPDATE=1`, `OPENCODE_DISABLE_MODELS_FETCH=1`, `XDG_DATA_HOME=/run/nexus/cred-dir`, `NODE_EXTRA_CA_CERTS=...`.
- `opencode auth list` (the v1.18.31 command; there is no separate auth-status):

```
┌  Credentials /run/nexus/cred-dir/opencode/auth.json
│
└  0 credentials

┌  Environment
│
●  OpenCode Zen OPENCODE_API_KEY
│
●  OpenCode Go OPENCODE_API_KEY
│
└  2 environment variables
```

Zero file credentials is expected: the seeded object is not a provider map. The env section is the guest channel, and it names both catalog providers that share `OPENCODE_API_KEY`.

Torn down with `nexus rm test/opencode-auth-check` (`removed sandbox test/opencode-auth-check`). The verification state dir under `/workspace/.opencode-live-state` was deleted afterwards. The earlier interrupted build's 6.4G directory was the same kind of leftover (dead builder sandbox, no live processes) and was deleted before this run.

## Wrong premises

- Prior comment said both the file key and `OPENCODE_API_KEY` talk only to `https://opencode.ai/zen/go/v1`. Zen uses `https://opencode.ai/zen/v1`. Same hostname, so the egress list does not change.
- The models catalog is published on models.dev, but the CLI fetches `https://models.opencode.ai` unless the disable flag is set. That host is not credentialed and is not in `EgressHosts`.
- A JWT-shaped placeholder is not required.
- This guest cannot see the operator's real `auth.json`, so the booted sandbox was checked against a synthetic key, not a copy of the host file. Preflight's refusal without that fixture is the evidence the importer does not invent a credential.
