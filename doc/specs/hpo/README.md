---
id: C-HPO
type: concept
title: Herdr plugin out-of-the-box
parent: C-NEXUS3
summary: "Requirements for zero-manual-step install and upgrade of the nexus3 herdr plugin on Linux and macOS, with a real-herdr test harness and focused port-forwards."
---

# Herdr plugin out-of-the-box (REQ-HPO-*)

Covers the end-to-end fresh-user experience for the nexus3 herdr plugin: one-command install on Linux x86-64 and macOS remote clients, idempotent upgrade, ABI/version skew reporting, automatic config.toml wiring, install-time substrate preflight, a real-herdr acceptance harness, and port-forward scoping to the focused workspace.

Charter trace prefix: `REQ-HPO-*` maps to spec nodes `HPO-R-001` … `HPO-R-011`.

Motive: `herdr-plugin-ootb`. Slices T1–T8 implement these requirements; T9 authored this spec; F4 added HPO-R-011.
