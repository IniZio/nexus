---
id: C-NEXUS3
type: concept
title: nexus3
parent: null
summary: "Requirement graph for the nexus3 microVM sandbox runtime, covering the parallel-dev-flow, resource-lifecycle, and surface-contract milestones."
---

# nexus3

nexus3 is a microVM-grade sandbox runtime for coding agents. This requirement graph covers the `nexus3-parallel-dev-pr-flow` milestone.

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
