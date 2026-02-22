#!/bin/bash
# Cleanup script for old documentation files
# Run from repository root: bash cleanup-old-docs.sh

echo "Cleaning up old documentation structure..."

# Delete old internal plans folder (consolidated into unified plans)
rm -rf docs/dev/internal/plans
echo "✓ Removed docs/dev/internal/plans"

# Delete old internal testing folder (moved to docs/testing)
rm -rf docs/dev/internal/testing
echo "✓ Removed docs/dev/internal/testing"

# Delete old implementation folder
rm -rf docs/dev/internal/implementation
echo "✓ Removed docs/dev/internal/implementation"

# Delete ARCHIVE folder (historical documents)
rm -rf docs/dev/internal/ARCHIVE
echo "✓ Removed docs/dev/internal/ARCHIVE"

# Delete old research folder (research can be kept if valuable, but removing for cleanliness)
rm -rf docs/dev/internal/research
echo "✓ Removed docs/dev/internal/research"

# Delete old dated plans at root level
rm -f docs/plans/2026-02-22-comprehensive-test-suite.md
rm -f docs/plans/2026-02-22-port-forwarding-compose.md
echo "✓ Removed old dated plan files"

# Delete old testing docs at dev level (moved to docs/testing)
rm -f docs/dev/testing/plugin-testing.md
rm -f docs/dev/testing/workspace-testing.md
echo "✓ Removed old dev testing docs"

# Remove empty internal folder
if [ -d "docs/dev/internal" ]; then
    rmdir docs/dev/internal 2>/dev/null || echo "Note: docs/dev/internal not empty or has subdirectories"
fi

# Remove empty plans folder at root
if [ -d "docs/plans" ]; then
    rmdir docs/plans 2>/dev/null || echo "Note: docs/plans not empty"
fi

echo ""
echo "Cleanup complete!"
echo ""
echo "New documentation structure:"
tree -L 3 docs/ 2>/dev/null || find docs -type f -name "*.md" | head -20
