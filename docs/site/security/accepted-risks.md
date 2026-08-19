---
title: "Known Risks"
description: "Accepted tradeoffs, their residual controls, and retired risks that are now closed"
---

# Known Risks

> Conscious tradeoffs, not oversights — each entry states what is accepted, why, and what residual control limits the exposure.

These are the live accepted risks for v1. Each entry identifies the threat, the reason it was accepted rather than mitigated, and the control that limits its blast radius. Entries in [Retired risks](#retired-risks) were previously open and are now closed.

```sh
# The exfiltration surface is the allowlist — keep it minimal
nexus3 create --allow-host api.example.com my-sandbox
```

<Badge type="warning" text="partial" /> — current implementation uses `nexus3 sandbox create`; see [CLI sandbox commands](/cli/sandbox-commands) for the mapping.

## Live accepted risks

### Pane latency — measurement deferred

**Risk**: Composed keystroke-to-echo pane latency has not been measured. nexus3 adds at least one extra network hop (vsock → host → guest) versus running directly on the host. This may produce noticeable lag in interactive pane sessions.

**Acceptance rationale**: The latency is a post-implementation measurement item. The architecture does not preclude optimization (direct vsock paths exist). Accepted pending a real measurement run.

**Scope**: Interactive pane sessions. CLI and non-interactive workloads are unaffected.

### Allowed-host exfiltration

**Risk**: A guest process can exfiltrate data by encoding it in requests to an *allowed* host. Example: an agent could POST data to `api.anthropic.com` that is not prompt content.

**Acceptance rationale**: v1's threat model is credential theft and lateral movement via unauthorized egress — both of which the allowlist + MITM combination directly addresses. Exfiltration via an allowed channel is explicitly out of scope for v1. See also: DoS/resource-governance and covert channels, both deferred.

**Residual control**: Under MITM, the guest holds only high-entropy placeholders. The allowlist is the control boundary — what hosts are permitted defines the exfiltration surface. Keep `AllowedHosts` minimal.

### No force-remove of a snapshot while children page from it

**Risk**: `snapshot rm` refuses while live child sandboxes are paging from the parent snapshot disk. There is no `--force` flag in v1. If a parent snapshot cannot be removed, disk space cannot be reclaimed until all children are torn down.

**Acceptance rationale**: An eager O(1) length check catches a truncated parent before any UFFD child can exist, so the only case the refusal blocks is removing a snapshot with healthy live children. Force-remove of a live parent is fundamentally unsafe (children would SIGBUS); the refusal is the correct behavior. Force-remove is deferred to a future version that can safely drain children.

### Live worktree mount — metadata overhead

**Risk**: The virtiofs transport that delivers the host worktree to the guest carries elevated metadata overhead compared to a virtio-blk disk. Measured values (D-PD-102/103, n=10 bench-redo on equal-footing `cp -a` copies): `git status` is **4.74× slower** than ext4 in-guest (ratio of means) and **~10× slower** than the 14.75 ms host-native baseline; metadata **writes** remain **~15–20×** slower. The earlier figure of "327 ms vs 16 ms (~20×)" was void — an `mke2fs -d` inode artifact caused git to re-hash 489 MB on one leg only. Build steps that create many small files would be penalized if run on the mounted path.

**Acceptance rationale**: Write-heavy directories (`node_modules`, `.next`, `target`, `dist`) are steered onto named volumes (`--mount-named kind=disk`) — per-sandbox virtio-blk images whose metadata never crosses the virtiofs transport (D-PD-99). The content that remains on the live mount is source files: predominantly read, occasionally written by the agent during editing. For that access pattern the overhead is tolerable.

**Mitigations**: Named volumes (`--mount-named`) for write-heavy paths. Fork and snapshot are refused on a mounted sandbox — the enforcement exists and names the offending host→guest pairs in the error.

### Live mount gives guest write access to the host worktree

**Risk**: The virtiofs mount is bidirectional. A running guest can write to any path in the mounted host directory that the host process owns. A compromised agent could modify source files, corrupt the git index, or delete staged commits.

**Acceptance rationale**: nexus3's threat model is credential theft and unauthorized egress — not host filesystem corruption. The mounted directory is a `git worktree` under orchestrator control; integrity can be verified after the agent exits via `git diff` or `git fsck`. The host process user, not root, owns the files; the guest cannot escalate beyond those permissions.

**Mitigations**: Each sandbox gets its own worktree — no two concurrent sandboxes share a mounted path. `fork` and `snapshot create` are refused on a mounted sandbox with an explicit error naming the offending host→guest pairs; the enforcement exists in the current binary.

## Retired risks

These were previously open and are now closed.

### Material credentials + writable mount — retired

**Former state**: The guest held short-lived `material` tokens on a writable credential mount.

**Closed by**: The target agent uses the OAuth bearer opaquely (opaque 108-char string, not a JWT; trusts the file's `expiresAt`), confirming that the MITM placeholder-substitution model is viable. The guest now holds only high-entropy placeholders. `material` and the writable credential mount are retired.

### Open-in-practice egress — retired

**Former state**: Egress default-deny existed only as an opt-in, host-wide, arm64-unsupported experiment.

**Closed by**: Default-deny per-sandbox hostname allowlists are now enforced host-side.

## Threat model

nexus3's threat model is a compromised or malicious in-guest agent. The perimeter defends against:

1. **Credential theft**: the guest holds only useless placeholders; real tokens never cross the guest boundary.
2. **Unauthorized lateral movement**: connections to hosts not on the allowlist are dropped at L4.

The model does **not** defend against a compromised host operator. nexus3 runs as an unprivileged user process; it does not add any host-side privilege escalation surface beyond what Cloud Hypervisor requires.
