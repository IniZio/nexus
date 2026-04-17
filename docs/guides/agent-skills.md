# Agent Skills

Nexus uses the [Agent Skills](https://agentskills.io) open standard for packaging procedural knowledge that agents can load on demand. Skills provide domain expertise, workflow automation, and repository-specific context.

## Skill Locations

Nexus ships two layers of skills:

### Project Skills (`.opencode/skills/`)

Automatically loaded by the opencode agent when working in this repository. Each skill is a directory containing a `SKILL.md` following the [Agent Skills spec](https://agentskills.io/specification).

| Skill | Description |
|---|---|
| `nexus-macos-dev` | Build, run, and test NexusApp.app and its embedded daemon |
| `bumping-app-version` | Bump or update the app version number |
| `creating-git-commits` | Create commits matching repository conventions |
| `superpowers` | Workspace superpowers (experimental) |

To use a project skill, an agent simply loads it when the matching task is detected. For example, when asked to build the macOS app, the agent loads `nexus-macos-dev` to get step-by-step instructions.

### Groundwork Workflow Skills

Loaded from `~/.config/opencode/plugins/groundwork/skills/`. These implement the Nexus development workflow (PRD creation, advisor gates, nested PRDs, etc.).

| Skill | When to Use |
|---|---|
| `create-prd` | Starting a non-trivial feature (≥1 day); no master PRD exists |
| `advisor-gate` | Any technical decision; always at task completion before declaring done |
| `nested-prd` | Architectural pivot or scope increase >1 day discovered mid-implementation |
| `bdd-implement` | Any visible UI change or bug fix on macOS or web |
| `session-continue` | Context window growing long or fresh session needed |
| `consolidate-docs` | Cleaning up PRDs after iterations; preparing for handoff or release |
| `opencode-acp` | Controlling another opencode instance via ACP protocol |

## Agent Skills Format

Each skill is a directory with at minimum a `SKILL.md` file:

```
skill-name/
├── SKILL.md          # Required: YAML frontmatter + Markdown instructions
├── scripts/          # Optional: executable helpers
├── references/       # Optional: detailed reference docs
└── assets/           # Optional: templates, schemas
```

### SKILL.md Frontmatter

```yaml
---
name: skill-name            # Required; lowercase, numbers, hyphens only
description: ...            # Required; 1-1024 chars; when to use this skill
license: Apache-2.0         # Optional
compatibility: ...          # Optional; environment requirements
metadata:                   # Optional; arbitrary key-value pairs
  author: example
  version: "1.0"
---
```

### Body Content

The Markdown body contains the skill instructions. Recommended sections:

- **Overview** — what the skill does
- **When to Use** — trigger conditions
- **Step-by-step instructions** — concrete steps
- **Examples** — input/output pairs
- **Common edge cases** — troubleshooting

Keep the main `SKILL.md` under 500 lines. Move detailed reference material to separate files under `references/`.

## For Contributors

### Using Skills

When working with opencode on Nexus:

1. The agent automatically loads relevant skills based on detected task type
2. If you need a specific skill explicitly, mention it in your request (e.g., "use the `nexus-macos-dev` skill to build the app")
3. Skills provide context that the agent loads on demand

### Extending Skills

To add a new project skill:

1. Create a new directory under `.opencode/skills/`
2. Add a `SKILL.md` with proper frontmatter (`name`, `description` at minimum)
3. Follow the [Agent Skills spec](https://agentskills.io/specification)
4. Keep instructions under 500 lines; use `references/` for deep dives

### Skill Validation

The [skills-ref](https://github.com/agentskills/agentskills/tree/main/skills-ref) tool validates skill format:

```bash
skills-ref validate ./path/to/skill
```

## Related

- Agent Skills spec: https://agentskills.io/specification
- Example skills: https://github.com/anthropics/skills
- Nexus CONTRIBUTING.md for development workflow