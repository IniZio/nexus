# 08 — Perimeter and Credentials

*Purpose: the `internal/core/perimeter` module — the composed egress stack, the MITM credential model, SSH identity, and the v1 boundary. Almost entirely vetted libraries; near-zero security-critical hand-rolling.*

## The seam: `internal/core/perimeter`

`internal/core/perimeter` is **one layered module**, a **peer of `driver`** (ticket 15). It consumes a `driver` **network-hook primitive** (a tap-fd on Linux; `VZFileHandleNetworkDeviceAttachment` on macOS) analogous to `DialGuest`: `driver` hands the raw substrate hook and absorbs the substrate asymmetry, so **above the fd `perimeter` is platform-uniform**. `driver` must not import `perimeter`; `service` consumes it. It also consumes the provisional reverse-dial `ListenGuest` (doc 02) for the forwarded ssh-agent socket.

**One perimeter per VM instance.** On fork, the hostname allowlist is **inherited frozen**; the DNS name→IP table is **rebuilt per child**. Live egress connections break on restore and the guest reconnects (the doc 04 / ticket 32/36 shape).

## The composed stack (vetted libraries)

Per the user directive that the security-critical plumbing be **composed from vetted libraries, not hand-rolled** (tickets 15, 42):

- **gvproxy** (`containers/gvisor-tap-vsock`, Apache-2.0) — the userspace network stack that owns **all guest packets** on both substrates. gVisor's `tcp.NewForwarder` is the per-connection accept/deny hook; DNS rides gvproxy's bundled `miekg/dns`.
- **copied clawk `netfilter.AllowList`** (Apache-2.0) — default-deny **L4 host:port** allowlist on every SYN/UDP/ICMP/DNS, plus `ObserveDNSAnswer` for the DNS name→IP table. clawk's `netfilter` is under Go `internal/`, so it is **copied with attribution, not imported**.
- **`elazarl/goproxy`** (BSD-3) — the **L7 TLS-MITM** proxy: a local CA + leaf minting + per-host `Authorization` rewrite, as an in-process `http.Handler` off a `net.Listener`. It transparently intercepts **all allowed HTTPS**, enforces the **hostname** allowlist on the real SNI/Host (closing the shared-CDN/IP hole that L4 IP-allowlisting alone leaves), and swaps placeholder→real bearer.
- **The only custom code is non-crypto:** a transparent **SNI→CONNECT shim** (an adversarial guest will not honour `HTTPS_PROXY`, so traffic must be intercepted transparently) plus the hook wiring.

Rejected: `gomitmproxy` (GPL), `martian` (archived).

## Credential model — MITM placeholder substitution (normative)

The normative model is **TLS-MITM placeholder substitution** (ticket 23, ratified live; verified by ticket 41). The earlier **`material` + writable-mount** model is a **retired interim** — do not describe it as current.

- The guest holds **only high-entropy placeholders** plus a **synthetic far-future `expiresAt`**, seeded **once** at start.
- For **allowed hosts only**, the per-sandbox-CA MITM proxy swaps **placeholder → real current bearer** on the wire. A placeholder sent to a non-allowed host is never swapped, so **the guest holds nothing worth stealing** — a placeholder is useless off the allowlist.
- **All dynamism is host-side.** The host broker keeps the real token fresh; the synthetic far-future expiry stops the agent self-refreshing. This eliminates the guest credential shim entirely: **the creds file is seeded once, there is no `oauth_shim`, no re-pull loop, and no guest→host vsock credential channel** (which is why ticket 23 released the credential claim on `ListenGuest`).
- **OAuth is solved without response-rewriting**: synthetic expiry stops self-refresh; the host keeps the real token fresh (proven: 6 rotations / 0 client refreshes).

**Verified gate (ticket 41):** the Anthropic subscription `accessToken` is an **opaque 108-char string, not a JWT** — Claude Code sends `Authorization: Bearer ${accessToken}` verbatim and compares the file's numeric `expiresAt`, never decoding the token. Both conditions the placeholder model needs hold. One caveat carried: a **lapsed/absent `expiresAt`** triggers a refreshToken-based refresh, which the synthetic far-future expiry must keep from firing.

## Credential file freshness

Because a newly-exec'd process opens the credential file and virtiofsd serves it from the **live host file**, freshness relies on **FUSE close-to-open consistency** — no guest-side signal is needed (ticket 27; guest-side inotify/fanotify does **not** see host-originated virtio-fs writes). This makes two things hard requirements:

- **`--cache=never`** on virtiofsd (its own default is `auto`).
- Host writes must be **write-temp-then-`rename(2)`**.

The honest guarantee (ticket 11 §7 corrected by ticket 27): freshness is **guaranteed for new process launches; no guarantee either way for already-running processes** (a long-lived process that re-reads the file mid-life *will* see new content, but nexus3 promises nothing about that).

Also: the per-sandbox **MITM CA is seeded** into the guest trust store at start (per-workspace CA, seeded not baked; doc 07).

## SSH identity

SSH identity is **`x/crypto/ssh/agent` agent-forward, bounded by the L4 allowlist** (ticket 15), via a **dedicated per-sandbox minimal-key agent**. This covers SSH-only-push repos and is threat-symmetric with the bearer swap. git→HTTPS rewrite is optional; **origination-splice and key-material-mount are rejected/deferred** (no maintained Go SSH-MITM library exists; agent-forward bounded by the allowlist is the library-friendly path). The guest carries SSH client config wiring `SSH_AUTH_SOCK` + `known_hosts` for allowlisted git hosts (doc 07).

## The v1 boundary (guarantee and non-goals)

**The guarantee is: allowlist + audit.** Default-deny per-sandbox **hostname** allowlists, enforced host-side, plus audit.

**Explicit v1 non-goals (backlogged, doc 11):**

- **allowed-host exfiltration** — a placeholder-bearing request to an *allowed* host can carry data out;
- **DoS / resource-governance**;
- **covert channels**.

Under MITM the guest holds only placeholders, so allowlist scoping — not the transport — is the residual control.

## Fork and the builder exception

- The builder VM needs **unrestricted egress** and does **not** share a workspace's default-deny posture (doc 07).
- macOS viability is settled (clawk runs gvproxy on the VZ attachment); the fd-pass-vs-relay placement folds into doc 12 (tickets 18, 33).

---

*Sources: tickets 15 (perimeter seam, composed stack, hostname allowlist, SSH agent-forward, SNI→CONNECT shim, v1 non-goals, one-perimeter-per-VM), 42 (library survey), 23 (MITM placeholder substitution, no guest shim/channel), 41 (bearer opacity verified; expiresAt caveat), 27 (`--cache=never`, rename(2), close-to-open, honest freshness rule), 20 §4/§11 (material retired). Map: material/writable-mount retired interim, open-egress retired.*
