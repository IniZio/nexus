---
id: C-PER
type: concept
title: Persistent perimeter
parent: C-NEXUS3
summary: "Requirements for the per-sandbox detached supervisor that keeps the egress perimeter and credential broker alive after the spawning CLI exits."
---

# Persistent perimeter (REQ-PER-*)

Covers the detached supervisor architecture: the hidden `nexus3 __supervisor`
subcommand, perimeter lifetime decoupled from the one-shot CLI, `orca create`
spawn + READY handshake + destroy teardown, Refresher-fed credential broker,
zero-cred-in-guest invariant, and orphan/liveness cleanup.

Charter trace prefix: `REQ-PER-*` maps to spec nodes `PER-R-001` … `PER-R-007`.
