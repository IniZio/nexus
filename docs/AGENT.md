# Documentation Standards for Agents

This document defines how to organize and maintain documentation in the Nexus project.

## Quick Reference

```
docs/
├── AGENT.md                    # This file - root standards
├── index.md                    # Documentation home
├── dev/
│   ├── AGENT.md               # Dev docs standards
│   ├── roadmap.md             # Current project status
│   ├── decisions/             # ADRs (Architecture Decision Records)
│   └── plans/        # PRDs and implementation plans
├── explanation/               # Concepts and architecture
├── reference/                 # API/CLI reference
├── tutorials/                 # Step-by-step guides
└── testing/                   # Testing documentation
```

## Folder Structure Rules

### 1. User-Facing vs Internal

**User-Facing** (`docs/` root level):
- Tutorials - how to use features
- Reference - API/CLI documentation
- Explanation - concepts and architecture
- Only document **IMPLEMENTED** features

**Internal** (`docs/dev/`):
- Roadmap - current project status
- Decisions - ADRs (accepted architectural decisions)
- Plans - PRDs for upcoming features

### 2. Naming Conventions

| Type | Pattern | Example |
|------|---------|---------|
| ADRs | `###-descriptive-name.md` | `001-worktree-isolation.md` |
| Plans | `###-descriptive-name.md` | `001-workspace-management.md` |
| Reference | `kebab-case.md` | `boulder-cli.md` |
| Tutorials | `kebab-case.md` | `plugin-setup.md` |

**NO dates in filenames** (e.g., NOT `2026-02-20-something.md`)
**NO mixed formats** (folders AND files for same topic)

### 3. Content Requirements

**Only document IMPLEMENTED features.**

If a feature doesn't exist:
- Don't create reference docs for it
- Don't add CLI commands that don't work
- Note it as "Planned" or "Not implemented" in roadmap
- Put plans in `docs/dev/plans/`

**Never document:**
- `nexus` CLI (doesn't exist yet)
- Remote workspaces via SSH (not implemented)
- Workspace lifecycle management (partial)

## When Things Change

### Adding New Documentation

1. Determine if user-facing or internal
2. Choose correct folder based on content type
3. Follow naming convention
4. Update relevant index/link files

### Updating Existing Docs

1. Update content in place
2. If moving: create redirect note in old location for 30 days
3. Update all internal links
4. Update roadmap status if component status changes

### Deprecating Features

1. Mark as deprecated in reference docs
2. Add deprecation notice with timeline
3. Update roadmap to reflect deprecation
4. Don't delete immediately - keep for 3 months

## Link Maintenance

Use **relative links** only:

```markdown
Good:  [Boulder System](../dev/boulder-system.md)
Bad:   [Boulder System](/docs/dev/boulder-system.md)
```

Before committing doc changes:
1. Verify all internal links work
2. Check for broken references
3. Update table of contents if structure changes

## Prohibited Patterns

❌ **Never do:**
- Create documentation for unimplemented features as if they exist
- Mix dates in filenames (use sequence numbers instead)
- Have both folder AND flat file for same topic
- Put user-facing docs in appropriate top-level folders (`tutorials/`, `reference/`, etc.)
- Reference external systems that aren't integrated

## Verification Checklist

Before claiming documentation task complete:

- [ ] All files follow naming conventions
- [ ] No documentation for unimplemented features
- [ ] Internal links verified
- [ ] Roadmap updated if component status changed
- [ ] AGENT.md standards followed
- [ ] No duplicate content between files

---

**Last Updated:** February 2026
