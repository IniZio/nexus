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
