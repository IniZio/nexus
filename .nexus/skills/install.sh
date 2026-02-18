#!/bin/bash
# Install nexus skills for your AI assistant

set -e

PLATFORM=${1:-"all"}

link_git_hooks() {
    echo "🔗 Linking git hooks..."
    mkdir -p .git/hooks
    if [ -f ".nexus/git-hooks/pre-commit" ]; then
        ln -sf ../../.nexus/git-hooks/pre-commit .git/hooks/pre-commit
        echo "   ✅ pre-commit hook linked"
    fi
    if [ -f ".nexus/git-hooks/post-commit" ]; then
        ln -sf ../../.nexus/git-hooks/post-commit .git/hooks/post-commit
        echo "   ✅ post-commit hook linked"
    fi
}

case "$PLATFORM" in
  opencode)
    mkdir -p .opencode/skills
    cp -r .nexus/skills/nexus-dogfooding .opencode/skills/
    link_git_hooks
    echo "✅ Installed for OpenCode"
    ;;
  claude)
    mkdir -p .claude/skills
    cp -r .nexus/skills/nexus-dogfooding .claude/skills/
    link_git_hooks
    echo "✅ Installed for Claude"
    ;;
  cursor)
    mkdir -p .cursor/skills
    cp -r .nexus/skills/nexus-dogfooding .cursor/skills/
    link_git_hooks
    echo "✅ Installed for Cursor"
    ;;
  codex)
    mkdir -p .codex/skills
    cp -r .nexus/skills/nexus-dogfooding .codex/skills/
    link_git_hooks
    echo "✅ Installed for Codex CLI"
    ;;
  copilot)
    mkdir -p .github/prompts
    cp -r .nexus/skills/nexus-dogfooding/PROMPT.md .github/prompts/nexus-dogfooding/PROMPT.md 2>/dev/null || cp .nexus/skills/nexus-dogfooding/SKILL.md .github/prompts/nexus-dogfooding/PROMPT.md
    link_git_hooks
    echo "✅ Installed for GitHub Copilot"
    ;;
  continue)
    mkdir -p .continue/skills
    cp -r .nexus/skills/nexus-dogfooding .continue/skills/
    link_git_hooks
    echo "✅ Installed for Continue"
    ;;
  windsurf)
    mkdir -p .windsurf/skills
    cp -r .nexus/skills/nexus-dogfooding .windsurf/skills/
    link_git_hooks
    echo "✅ Installed for Windsurf"
    ;;
  roo)
    mkdir -p .roo/skills
    cp -r .nexus/skills/nexus-dogfooding .roo/skills/
    link_git_hooks
    echo "✅ Installed for Roo Code"
    ;;
  kiro)
    mkdir -p .kiro/skills
    cp -r .nexus/skills/nexus-dogfooding .kiro/skills/
    link_git_hooks
    echo "✅ Installed for Kiro"
    ;;
  hooks)
    link_git_hooks
    echo "✅ Git hooks linked"
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
    echo "Usage: ./install.sh [opencode|claude|cursor|codex|copilot|continue|windsurf|roo|kiro|hooks|all]"
    exit 1
    ;;
esac
