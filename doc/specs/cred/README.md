---
id: C-CRED
type: concept
title: Credential delivery and SSH relay
parent: C-NEXUS3
summary: "Requirements for live-mount credential delivery to claude-code sandboxes, auto permission mode, git SSH relay with policy guard, and port auto-forward."
---

# Credential delivery and SSH relay (REQ-CRED-*)

Covers the `nexus3-mount-creds-ssh-relay` motive: live virtiofs mount of the host `~/.claude` directory, CredGuardian proactive refresh, auto permission mode (no `--dangerously-skip-permissions`), git over SSH via vsock relay with policy guard and branch enforcement, brokered `GH_TOKEN` regression guard, and port auto-forward with herdr ≥ 0.9.

Charter trace prefix: `REQ-CRED-*` maps to spec nodes `CRED-R-001` … `CRED-R-006`.
