#!/bin/bash
# Install nexus skills for your AI assistant

set -e

PLATFORM=${1:-"all"}

case "$PLATFORM" in
  opencode)
    mkdir -p .opencode/skills
    cp -r .nexus/skills/nexus-dogfooding .opencode/skills/
    echo "✅ Installed for OpenCode"
    ;;
  claude)
    mkdir -p .claude/skills
    cp -r .nexus/skills/nexus-dogfooding .claude/skills/
    echo "✅ Installed for Claude"
    ;;
  cursor)
    mkdir -p .cursor/skills
    cp -r .nexus/skills/nexus-dogfooding .cursor/skills/
    echo "✅ Installed for Cursor"
    ;;
  codex)
    mkdir -p .codex/skills
    cp -r .nexus/skills/nexus-dogfooding .codex/skills/
    echo "✅ Installed for Codex CLI"
    ;;
  copilot)
    mkdir -p .github/prompts
    cp -r .nexus/skills/nexus-dogfooding/PROMPT.md .github/prompts/nexus-dogfooding/PROMPT.md 2>/dev/null || cp .nexus/skills/nexus-dogfooding/SKILL.md .github/prompts/nexus-dogfooding/PROMPT.md
    echo "✅ Installed for GitHub Copilot"
    ;;
  continue)
    mkdir -p .continue/skills
    cp -r .nexus/skills/nexus-dogfooding .continue/skills/
    echo "✅ Installed for Continue"
    ;;
  windsurf)
    mkdir -p .windsurf/skills
    cp -r .nexus/skills/nexus-dogfooding .windsurf/skills/
    echo "✅ Installed for Windsurf"
    ;;
  roo)
    mkdir -p .roo/skills
    cp -r .nexus/skills/nexus-dogfooding .roo/skills/
    echo "✅ Installed for Roo Code"
    ;;
  kiro)
    mkdir -p .kiro/skills
    cp -r .nexus/skills/nexus-dogfooding .kiro/skills/
    echo "✅ Installed for Kiro"
    ;;
  all)
    ./install.sh opencode
    ./install.sh claude
    ./install.sh cursor
    ./install.sh codex
    ./install.sh copilot
    ./install.sh continue
    ./install.sh windsurf
    ./install.sh roo
    ./install.sh kiro
    echo "✅ Installed for all platforms"
    ;;
  *)
    echo "Unknown platform: $PLATFORM"
    echo "Usage: ./install.sh [opencode|claude|cursor|codex|copilot|continue|windsurf|roo|kiro|all]"
    exit 1
    ;;
esac
