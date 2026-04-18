# Operations playbook

Short reference for **latency**, **isolation concepts**, and **paths**.

## Doctor vs backend

- **`nexus doctor`** runs from CWD (no flags required to specify project root). There is no top-level `--timeout`; probes use internal timeouts.
- On startup, the CLI prints **`doctor: runtime backend=…`** so you know whether the runtime is **firecracker** or **process** (or other supported backend).
- **Firecracker, cold VM:** the first run can take **several minutes** (guest bootstrap, Docker/tooling) before your `.nexus/probe` scripts run. Silence is often normal.
- **Process sandbox** (fallback when VM is unavailable): usually **much faster** for the same project.
- Predicting backend: see `nexus create --backend …` and host capabilities. See [Workspace config](../reference/workspace-config.md).

## Isolation: fork vs workspace vs git worktree

| Mechanism | What it isolates | Typical use |
|-----------|------------------|-------------|
| **Git worktree** | Second checkout + branch on the **same machine** | Parallel features without branch switching in one tree. |
| **New Nexus workspace (`create`)** | Separate workspace id, runtime, and often VM | Remote execution, different repos/refs, clean processes. |
| **`fork`** | Child workspace derived from a parent (product semantics) | Experiment from a snapshot; check current docs for auth-bundle and metadata. |

Worktrees do **not** replace Nexus workspaces for remote sandboxes; they solve different problems.

## Remote Daemon (nexusd on Linux)

Run `nexusd` on a remote Linux host so that a local `nexus` client can connect to it over the network.

### Prerequisites

- Linux x86-64
- Go 1.22+ (to build from source) **or** a pre-built `nexusd` binary
- `openssl` (for token generation)
- Port 7777 reachable from the client, or an SSH tunnel

### Build from source

```bash
git clone https://github.com/inizio/nexus ~/magic/nexus
cd ~/magic/nexus/packages/nexus
go build -o ~/magic/bin/nexusd ./cmd/nexusd
export PATH="$HOME/magic/bin:$PATH"
```

### Generate a bearer token

```bash
openssl rand -hex 32
# example output: a3f9c2e1b4d78f0a5c6e3b1d2a9f7e4c8b5d3a1e6c9f2b4d7a0e3c8f1b5d9a2e
```

Save this value — you will need it on both the server and the client.

### Start nexusd with a network listener

**Public / direct TCP (add firewall rule for port 7777):**

```bash
nexusd --network --bind 0.0.0.0 --port 7777 --token <token>
```

**Loopback + SSH tunnel (no firewall change needed, no TLS required):**

```bash
# On the Linux host:
nexusd --network --bind 127.0.0.1 --port 7777 --token <token>

# On the client machine, forward the remote port locally:
ssh -N -L 7777:127.0.0.1:7777 user@remote-host
```

### TLS (non-loopback deployments)

Use `--tls required` with a certificate and key for direct public exposure:

```bash
nexusd --network --bind 0.0.0.0 --port 7777 --token <token> \
  --tls required --tls-cert /etc/nexus/cert.pem --tls-key /etc/nexus/key.pem
```

Use `--tls auto` for a self-signed certificate (clients must accept the cert):

```bash
nexusd --network --bind 0.0.0.0 --port 7777 --token <token> --tls auto
```

The default (`--tls off`) sends traffic in plaintext — safe only over loopback or SSH tunnels.

### Systemd unit (persistent service)

Create `/etc/systemd/system/nexusd.service` (or `~/.config/systemd/user/nexusd.service` for a user unit):

```ini
[Unit]
Description=Nexus Daemon
After=network.target

[Service]
ExecStart=/home/<user>/magic/bin/nexusd \
  --network \
  --bind 127.0.0.1 \
  --port 7777 \
  --token <token>
Restart=on-failure
RestartSec=5s
WorkingDirectory=/home/<user>/magic

[Install]
WantedBy=multi-user.target
```

Enable and start:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now nexusd
sudo systemctl status nexusd
```

### Firewall

If binding to `0.0.0.0`, open port 7777:

```bash
# ufw
sudo ufw allow 7777/tcp

# firewalld
sudo firewall-cmd --permanent --add-port=7777/tcp && sudo firewall-cmd --reload

# iptables
sudo iptables -A INPUT -p tcp --dport 7777 -j ACCEPT
```

If using loopback + SSH tunnel, no firewall change is required.

### Health and version checks

```bash
curl http://localhost:7777/healthz
# {"status":"ok"}

curl http://localhost:7777/version
# {"version":"dev"}
```

For TLS deployments, use `https://` and pass `-k` (or `--cacert`) as appropriate.

### Validation checklist

- [ ] `systemctl status nexusd` shows `active (running)`
- [ ] `curl http://localhost:7777/healthz` returns `{"status":"ok"}`
- [ ] `curl http://localhost:7777/version` returns a version object
- [ ] Token is set in the client's `NEXUS_DAEMON_TOKEN` env var or profile config
- [ ] Client `nexus list` connects without authentication errors

## Related

- [Host auth bundle](../reference/host-auth-bundle.md)
- [CLI reference](../reference/cli.md)
- [Installation](installation.md)
