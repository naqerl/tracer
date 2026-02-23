#!/bin/bash
# Comprehensive crawler that also tests markdown format

BASE_URL="http://localhost:18081"
COOKIE_JAR="/tmp/crawl-cookies.txt"

count=0
errors=0

check_url() {
    local url=$1
    local desc=$2
    
    count=$((count + 1))
    
    # Check normal request
    STATUS=$(curl -s -o /dev/null -w "%{http_code}" "$BASE_URL$url" 2>/dev/null)
    
    if [ "$STATUS" = "500" ]; then
        echo "❌ FAIL: $url -> 500 ERROR ($desc)"
        errors=$((errors + 1))
    elif [ "$STATUS" = "000" ]; then
        echo "❌ FAIL: $url -> Connection error ($desc)"
        errors=$((errors + 1))
    else
        echo "✅ OK: $url -> $STATUS ($desc)"
    fi
}

echo "=========================================="
echo "Comprehensive Panel Route Testing"
echo "=========================================="
echo ""

# Public routes
echo "--- Public Routes ---"
check_url "/" "Landing page"
check_url "/login" "Login page"
check_url "/llm" "LLM docs"

# Panel routes (will redirect to login)
echo ""
echo "--- Panel Routes ---"
check_url "/panel/" "Dashboard"
check_url "/panel/collectors" "Dashboard redirect"
check_url "/panel/collectors/demo-alpha" "Collector overview"
check_url "/panel/collectors/demo-alpha/resources" "Collector resources"
check_url "/panel/collectors/demo-alpha/apps" "Collector apps"
check_url "/panel/collectors/demo-alpha/apps/alpha-json" "App analytics"
check_url "/panel/collectors/demo-alpha/apps/alpha-json/logs" "App logs"
check_url "/panel/collectors/demo-alpha/apps/alpha-json/logs/groups/1" "Log group detail"

# Settings routes
echo ""
echo "--- Settings Routes ---"
check_url "/settings/" "Settings page"
check_url "/settings/entries" "Settings entries"
check_url "/settings/notifiers" "Notifiers list"
check_url "/settings/api-key" "API key"

# Ingest API
echo ""
echo "--- Ingest API ---"
check_url "/ingest/collectors" "List collectors (API)"

echo ""
echo "=========================================="
echo "Total routes tested: $count"
echo "Errors found: $errors"
echo "=========================================="

if [ $errors -eq 0 ]; then
    echo "✅ All routes passed - no 500 errors!"
    exit 0
else
    echo "❌ Found $errors error(s)!"
    exit 1
fi
