# Remote Profile Validation Checklist

Step-by-step guide for manually validating the remote daemon profile flow in the NexusApp macOS client.

**Prerequisites:** NexusApp installed, access to a Linux host (or local loopback + SSH tunnel), `nexusd` binary available.

---

## 1. Daemon Setup (Linux host)

- [ ] Build or install `nexusd` on the Linux host per [operations.md](operations.md)
- [ ] Generate a bearer token: `openssl rand -hex 32` — note it for use below
- [ ] Start nexusd (public bind with TLS, or loopback for SSH-tunnel):
  ```bash
  # Public bind (requires TLS — use auto for self-signed):
  nexusd --network --bind 0.0.0.0 --port 7777 --token <token> --tls auto

  # Loopback + SSH tunnel (no TLS required):
  nexusd --network --bind 127.0.0.1 --port 7777 --token <token>
  ```
- [ ] **PASS:** Process stays running, no startup error in stderr
- [ ] Verify health (no token needed):
  ```bash
  curl http://<host>:7777/healthz
  # Expected: {"status":"ok"}  HTTP 200
  ```
- [ ] Verify version (no token needed):
  ```bash
  curl http://<host>:7777/version
  # Expected: {"version":"dev"}  HTTP 200
  ```
- [ ] **PASS:** Both return HTTP 200 with JSON body

---

## 2. App Profile Creation

- [ ] Open NexusApp → **Daemon Settings**
- [ ] Tap **+** / **Add Profile**
- [ ] Fill in:
  - Name: any label (e.g. `remote-dev`)
  - Mode: **Remote**
  - Host: `<linux-host-ip>` (or `127.0.0.1` for SSH-tunnel)
  - Port: `7777`
  - Scheme: `ws` (or `wss` if TLS enabled)
  - Token source: **Manual** → paste the token from step 1
- [ ] Tap **Save**
- [ ] **PASS:** Profile appears in the list with no validation error

---

## 3. Test Connection

- [ ] With the new profile selected, tap **Test Connection**
- [ ] **PASS:** Success indicator shown (no error banner)
- [ ] Edit the profile: change token to something wrong, save, test again
- [ ] **PASS:** Explicit error shown (e.g. "Unauthorized" or "Connection failed")
- [ ] **PASS:** App does NOT silently fall back to local daemon — local daemon process should not start
- [ ] Restore the correct token, save, test again
- [ ] **PASS:** Success indicator shown again

---

## 4. Remote Mode Activation

- [ ] Select the remote profile as **active/default**
- [ ] Quit and relaunch NexusApp (to verify profile persists across restarts)
- [ ] **PASS:** App reconnects to remote endpoint on launch — no local daemon process spawned
- [ ] **PASS:** App displays the remote profile name or a remote-mode indicator in Daemon Settings
- [ ] Workspace list loads (or shows an empty state — not a crash or error banner)

---

## 5. Error Cases

### 5a. Daemon unreachable
- [ ] Stop `nexusd` on the Linux host
- [ ] Observe the app state
- [ ] **PASS:** App shows a connection error state (banner / status indicator) — not a crash
- [ ] **PASS:** App does NOT fall back to starting a local daemon

### 5b. Recovery after reconnect
- [ ] Restart `nexusd` with the same token
- [ ] Tap **Test Connection** (or wait for auto-reconnect if implemented)
- [ ] **PASS:** Connection succeeds, error state clears

### 5c. Wrong port
- [ ] Edit profile: change port to `9999` (unused), save
- [ ] **PASS:** Explicit "connection refused" or "unreachable" error shown
- [ ] **PASS:** No silent fallback to local mode

---

## 6. Switch Back to Local Mode

- [ ] Open Daemon Settings, select the **Local** profile
- [ ] **PASS:** App switches to local mode and starts managing the local daemon process
- [ ] **PASS:** No stale remote connection state persists (no error banners from old remote profile)
- [ ] **PASS:** Workspace operations work against the local daemon

---

## 7. Token Source Variants

### 7a. Manual (inline `SecureField`)
- [ ] Already tested in sections 2–5 above
- [ ] **PASS:** Token is stored securely (not visible in plain text in UI after save)

### 7b. Environment variable
- [ ] Set `NEXUS_TOKEN=<your-token>` in the shell that launches NexusApp
- [ ] Create a profile with Token source **Env var** → variable name `NEXUS_TOKEN`
- [ ] Test connection
- [ ] **PASS:** Connection succeeds using the env var token

### 7c. Keychain (macOS Keychain)
- [ ] Add the token to macOS Keychain: `security add-generic-password -s nexus-remote -a nexus -w <token>`
- [ ] Create a profile with Token source **Keychain** → service name `nexus-remote`
- [ ] Test connection
- [ ] **PASS:** Connection succeeds using the keychain token
- [ ] **PASS:** Token is not visible in the profile settings UI

---

## Notes

- Items in **section 1** require a real Linux host or a local loopback with SSH tunnel.
- Loopback + SSH tunnel avoids TLS requirements: `ssh -L 7777:127.0.0.1:7777 user@host` then connect to `127.0.0.1:7777` with scheme `ws`.
- Pre-existing `Info.plist` warnings in Swift build output are harmless.
- The `cmd/nexus/` CLI has pre-existing broken imports and is not part of this flow.
