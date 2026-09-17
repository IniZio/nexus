---
id: C-NEXUS
type: concept
title: nexus
parent: null
summary: "Requirement graph for the nexus microVM sandbox runtime, covering the parallel-dev-flow, resource-lifecycle, and surface-contract milestones."
---

# nexus

nexus is a microVM-grade sandbox runtime for coding agents. This requirement graph covers the `nexus-parallel-dev-pr-flow` milestone.

Three sub-areas map to the charter's trace annotations:

| Sub-concept | Charter prefix | Range |
|---|---|---|
| C-PDF | REQ-PDF-* | Parallel-dev flow requirements |
| C-RES | REQ-RES-* | Resource lifecycle requirements |
| C-SUR | REQ-SUR-* | Surface contract requirements |

The charter's `trace: REQ-PDF-NNN` annotations correspond to `PDF-R-NNN` spec node IDs.

## Goals

- Formal traceability from every charter AC to a verifiable requirement node
- `spec build` and `spec lint` pass clean on this repo
