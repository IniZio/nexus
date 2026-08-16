# Accepted Risks

This register carries forward the v1 accepted-risk record from spec 11. It is a normative document: entries here represent conscious tradeoffs, not oversights.

## Live accepted risks (v1)

### (i) Pane latency — deferred measurement

**Risk**: Composed keystroke-to-echo pane latency has not been measured. The concern: nexus3 adds at least one extra network hop (vsock → host → guest) versus running directly on the host. This may produce noticeable lag in interactive pane sessions.

**Acceptance rationale**: The latency is a post-implementation measurement item. The architecture does not preclude optimization (direct vsock paths exist). Accepted pending a real measurement run.

**Scope**: Interactive pane sessions. CLI and non-interactive workloads are unaffected.

### (ii) Allowed-host exfiltration

**Risk**: A guest process can exfiltrate data by encoding it in requests to an *allowed* host. Example: an agent could POST data to `api.anthropic.com` that is not prompt content.

**Acceptance rationale**: v1's threat model is credential theft and lateral movement via unauthorized egress — both of which the allowlist + MITM combination directly addresses. Exfiltration via an allowed channel is explicitly out of scope for v1. See also: DoS/resource-governance and covert channels, both deferred.

**Residual control**: Under MITM, the guest holds only high-entropy placeholders. The allowlist is the control boundary — what hosts are permitted defines the exfiltration surface. Keep `AllowedHosts` minimal.

### (iii) No force-remove of a snapshot while children page from it

**Risk**: `snapshot rm` refuses while live child sandboxes are paging from the parent snapshot disk. There is no `--force` flag in v1. If a parent snapshot cannot be removed, disk space cannot be reclaimed until all children are torn down.

**Acceptance rationale**: An eager O(1) length check catches a truncated parent before any UFFD child can exist, so the only case the refusal blocks is removing a snapshot with healthy live children. Force-remove of a live parent is fundamentally unsafe (children would SIGBUS); the refusal is the correct behavior. Force-remove is deferred to a future version that can safely drain children.

## Retired gates

These risks were accepted as interim gates and are now closed.

### Material credentials + writable mount — RETIRED

**Former interim**: The guest held short-lived `material` tokens on a writable credential mount.

**Closed by**: Ticket 41 verified that the target agent uses the OAuth bearer opaquely (opaque 108-char string, not a JWT; trusts the file's `expiresAt`). This confirmed ticket 23's MITM placeholder-substitution model is viable. The guest now holds only high-entropy placeholders. `material` and the writable credential mount are retired.

### Open-in-practice egress — RETIRED

**Former interim**: Egress default-deny existed only as an opt-in, host-wide, arm64-unsupported experiment.

**Closed by**: Ticket 15 ships default-deny per-sandbox hostname allowlists enforced host-side. Gate closed.

## Threat model note

nexus3's threat model is a compromised or malicious in-guest agent. The perimeter defends against:

1. **Credential theft**: the guest holds only useless placeholders; real tokens never cross the guest boundary.
2. **Unauthorized lateral movement**: connections to hosts not on the allowlist are dropped at L4.

The model does **not** defend against a compromised host operator. nexus3 runs as an unprivileged user process; it does not add any host-side privilege escalation surface beyond what Cloud Hypervisor requires.
