# Developer Documentation Standards

Standards for maintaining developer documentation in `docs/dev/`.

## Folder Structure

```
docs/dev/
├── AGENT.md                   # This file
├── roadmap.md                 # Project status and component overview
├── decisions/                 # ADRs (Architecture Decision Records)
│   ├── 001-worktree-isolation.md
│   ├── 002-port-allocation.md
│   └── 003-telemetry-design.md
├── plans/                     # PRDs and implementation plans
│   ├── 001-workspace-management.md
│   ├── 002-telemetry.md
│   └── 003-nexus-cli.md
└── research/                  # Research findings (optional)
```

## Document Types

### Architecture Decision Records (ADRs)

**Location:** `docs/dev/decisions/`

**When to create:**
- Significant architectural choices
- Technology selections
- Design pattern adoptions

**Format:**
```markdown
# ADR-###: Title

**Status:** Proposed | Accepted | Deprecated | Superseded

## Context
Why we need this decision

## Decision
What we decided

## Consequences
Benefits and trade-offs

## Implementation
Code example or approach

## Related
Links to other ADRs
```

### Plans / PRDs

**Location:** `docs/dev/plans/`

**When to create:**
- Before implementing major features
- When planning new components
- For tracking implementation progress

**Format:**
```markdown
# Feature Name PRD

**Status:** Draft | In Progress | Complete | Cancelled
**Created:** YYYY-MM-DD
**Component:** Component Name

## 1. Overview
### 1.1 Problem Statement
### 1.2 Goals
### 1.3 Non-Goals

## 2. Architecture
## 3. Features
## 4. Implementation Plan
## 5. Open Questions
```

**Numbering:**
- Use sequential numbers (001, 002, 003...)
- Numbers don't indicate priority or order
- Never reuse numbers

### Roadmap

**Location:** `docs/dev/roadmap.md`

**Update when:**
- Component status changes
- New components added
- Features completed
- Links need fixing

**Status Emojis:**
- ✅ Implemented - Production ready
- 🚧 In Progress - Under active development
- 📋 Planned - Designed but not started
- ❌ Cancelled - Will not implement

## Research Documents

For deep-dive research that informs decisions but isn't part of the main documentation flow.

**When to use:**
- Technology comparisons
- Proof-of-concept results
- Investigation findings

**Note:** Research documents can be placed in `docs/dev/internal/implementation/` or create a `research/` subfolder if needed.

## What NOT to Document

Don't create or maintain:

1. **Implementation plans after completion** - Move key info to reference docs
2. **Research that didn't influence decisions** - Delete after 6 months
3. **Outdated ADRs** - Mark as superseded, don't delete
4. **Meeting notes** - Keep in shared drives, not docs
5. **Task tracking** - Use issues/PRs, not markdown

## Maintenance Rules

### Quarterly Review

Every 3 months, review:
- [ ] Roadmap accuracy
- [ ] ADR statuses (still accurate?)
- [ ] Plan completion status
- [ ] Broken links

### Archival Policy

- **Completed plans:** Keep for reference, mark complete
- **Cancelled plans:** Keep, mark cancelled (lessons learned)
- **Superseded ADRs:** Keep, mark superseded with link to new ADR
- **Old research:** Delete after 6 months if not referenced

## File Naming

| Document Type | Pattern | Example |
|--------------|---------|---------|
| ADR | `###-kebab-case.md` | `001-worktree-isolation.md` |
| Plan | `###-kebab-case.md` | `002-telemetry.md` |
| Research | `kebab-case.md` | `ssh-bridge-guide.md` |

**Rules:**
- Use sequence numbers for ADRs and plans
- Use kebab-case (lowercase with hyphens)
- NO dates in filenames
- NO underscores (use hyphens)

## Verification

Before submitting doc changes:

```bash
# Check naming conventions
ls docs/dev/decisions/  # Should be ###-name.md
ls docs/dev/internal/plans/      # Should be ###-name.md

# Check for broken links
# (Manual: search for .md references and verify they exist)

# Verify roadmap accuracy
grep "Implemented" docs/dev/roadmap.md  # Should match reality
```

---

**Last Updated:** February 2026
