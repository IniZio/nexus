# OpenCode Configuration

This directory contains project-specific OpenCode extensions.

## Configuration Location

**Main config is at:** `../opencode.json` (project root)

## Contents

- **plugins/**: OpenCode plugins (loaded from opencode.json)
- **skills/**: Agent skills for various AI assistants

## Current Plugins

- **nexus-opencode**: Local enforcement plugin for nexus workflows
  - Configured in: `../opencode.json`
  - Location: `../packages/opencode`
  - Enforces workspace usage, dogfooding, and task completion
