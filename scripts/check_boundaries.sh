#!/bin/bash
# Import boundary check script for Tracer project
# Fails if forbidden imports are detected between domains

set -e

echo "Checking import boundaries..."

# Track violations
VIOLATIONS=0

# Check that collector doesn't import panel
echo "Checking collector -> panel imports..."
if grep -r "github.com/ai-shift/tracer/apps/panel" apps/collector/ 2>/dev/null || \
   grep -r '"github.com/ai-shift/tracer/(config/panel|core/services/(auth|collectordb|dbmanager|notifier|retention))"' apps/collector/ 2>/dev/null; then
    echo "ERROR: Collector imports panel internals!"
    VIOLATIONS=$((VIOLATIONS + 1))
else
    echo "  OK - No panel imports in collector"
fi

# Check that landing doesn't import panel
echo "Checking landing -> panel imports..."
if grep -r "github.com/ai-shift/tracer/apps/panel" apps/landing/ 2>/dev/null || \
   grep -r '"github.com/ai-shift/tracer/(config/panel|core/services|db)"' apps/landing/ 2>/dev/null; then
    echo "ERROR: Landing imports panel internals!"
    VIOLATIONS=$((VIOLATIONS + 1))
else
    echo "  OK - No panel imports in landing"
fi

# Check that shared doesn't import any app domain
echo "Checking shared -> apps imports..."
if grep -r "github.com/ai-shift/tracer/apps/" libs/shared/ 2>/dev/null; then
    echo "ERROR: Shared imports app domains!"
    VIOLATIONS=$((VIOLATIONS + 1))
else
    echo "  OK - No app imports in shared"
fi

# Check that no root-level business code exists outside domains
echo "Checking root cleanliness..."
if [ -d "cmd" ] && [ "$(ls -A cmd 2>/dev/null)" ]; then
    echo "WARNING: Root cmd/ directory still exists (should be migrated to apps/*/cmd/)"
fi

if [ -d "core" ] && [ "$(ls -A core 2>/dev/null)" ]; then
    echo "WARNING: Root core/ directory still exists (should be migrated to apps/*/internal/ or libs/shared/)"
fi

if [ -d "config" ] && [ "$(ls -A config 2>/dev/null)" ]; then
    echo "WARNING: Root config/ directory still exists (should be migrated to apps/*/internal/config/)"
fi

if [ -d "web" ] && [ "$(ls -A web 2>/dev/null)" ]; then
    echo "WARNING: Root web/ directory still exists (should be migrated to apps/panel/internal/ or apps/landing/)"
fi

# Summary
echo ""
if [ $VIOLATIONS -eq 0 ]; then
    echo "✓ All import boundaries respected"
    exit 0
else
    echo "✗ Found $VIOLATIONS import boundary violation(s)"
    exit 1
fi
