#!/bin/bash
# Comprehensive crawler that discovers and tests ALL routes

BASE_URL="http://localhost:18081"
COOKIE_JAR="/tmp/crawl-all-cookies.txt"
USERNAME="demo"
PASSWORD="demo123"

count=0
errors=0
success=0

# Login first
echo "Logging in with demo credentials..."
curl -s -c "$COOKIE_JAR" -X POST "$BASE_URL/login" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -H "HX-Request: true" \
  -d "username=$USERNAME&password=$PASSWORD" \
  -o /dev/null 2>/dev/null

if grep -q "tracer_session" "$COOKIE_JAR" 2>/dev/null; then
    echo "✅ Login successful"
else
    echo "❌ Login failed"
    exit 1
fi

check_url() {
    local method=$1
    local url=$2
    local desc=$3
    
    count=$((count + 1))
    
    if [ "$method" = "POST" ]; then
        STATUS=$(curl -s -o /dev/null -w "%{http_code}" -X POST -b "$COOKIE_JAR" "$BASE_URL$url" 2>/dev/null)
    else
        STATUS=$(curl -s -o /dev/null -w "%{http_code}" -b "$COOKIE_JAR" "$BASE_URL$url" 2>/dev/null)
    fi
    
    if [ "$STATUS" = "500" ]; then
        echo "❌ FAIL: $method $url -> 500 ($desc)"
        errors=$((errors + 1))
    elif [ "$STATUS" = "000" ]; then
        echo "❌ FAIL: $method $url -> Connection error ($desc)"
        errors=$((errors + 1))
    elif [ "$STATUS" = "200" ] || [ "$STATUS" = "302" ] || [ "$STATUS" = "303" ]; then
        echo "✅ OK: $method $url -> $STATUS ($desc)"
        success=$((success + 1))
    else
        echo "⚠️  INFO: $method $url -> $STATUS ($desc)"
        success=$((success + 1))
    fi
}

echo ""
echo "=========================================="
echo "Comprehensive Route Crawler"
echo "=========================================="
echo ""

# Public routes
echo "--- Public Routes ---"
check_url "GET" "/" "Landing page"
check_url "GET" "/login" "Login page"
check_url "GET" "/llm" "LLM docs"

# Panel - Dashboard
echo ""
echo "--- Panel: Dashboard ---"
check_url "GET" "/panel/" "Dashboard"
check_url "GET" "/panel/collectors" "Dashboard redirect"

# Panel - Collector demo-alpha
echo ""
echo "--- Panel: Collector demo-alpha ---"
check_url "GET" "/panel/collectors/demo-alpha" "Collector overview"
check_url "GET" "/panel/collectors/demo-alpha/resources" "Resources"
check_url "GET" "/panel/collectors/demo-alpha/apps" "Apps list"

# Panel - Collector demo-alpha apps
echo ""
echo "--- Panel: demo-alpha Apps ---"
check_url "GET" "/panel/collectors/demo-alpha/apps/alpha-json" "App alpha-json analytics"
check_url "GET" "/panel/collectors/demo-alpha/apps/alpha-json/logs" "App alpha-json logs"
check_url "GET" "/panel/collectors/demo-alpha/apps/alpha-json/logs/groups/1" "App alpha-json log group 1"
check_url "GET" "/panel/collectors/demo-alpha/apps/alpha-regex" "App alpha-regex analytics"
check_url "GET" "/panel/collectors/demo-alpha/apps/alpha-regex/logs" "App alpha-regex logs"

# Panel - Collector demo-beta
echo ""
echo "--- Panel: Collector demo-beta ---"
check_url "GET" "/panel/collectors/demo-beta" "Collector overview"
check_url "GET" "/panel/collectors/demo-beta/resources" "Resources"
check_url "GET" "/panel/collectors/demo-beta/apps" "Apps list"

# Panel - Collector demo-beta apps
echo ""
echo "--- Panel: demo-beta Apps ---"
check_url "GET" "/panel/collectors/demo-beta/apps/beta-json" "App beta-json analytics"
check_url "GET" "/panel/collectors/demo-beta/apps/beta-json/logs" "App beta-json logs"
check_url "GET" "/panel/collectors/demo-beta/apps/beta-regex" "App beta-regex analytics"
check_url "GET" "/panel/collectors/demo-beta/apps/beta-regex/logs" "App beta-regex logs"

# Settings - Main
echo ""
echo "--- Settings: Main ---"
check_url "GET" "/settings/" "Settings page"
check_url "GET" "/settings/entries" "Settings entries"

# Settings - Users
echo ""
echo "--- Settings: Users ---"
check_url "GET" "/settings/users" "Users list"
check_url "GET" "/settings/users/1/apps" "User 1 apps"
check_url "GET" "/settings/users/1/apps/entries" "User 1 apps entries"

# Settings - API Key
echo ""
echo "--- Settings: API Key ---"
check_url "GET" "/settings/api-key" "API key"

# Settings - Notifiers
echo ""
echo "--- Settings: Notifiers ---"
check_url "GET" "/settings/notifiers" "Notifiers list"

# Ingest API (needs API key, will fail with auth)
echo ""
echo "--- Ingest API (requires API key) ---"
check_url "GET" "/ingest/collectors" "List collectors (401 expected)"
check_url "GET" "/ingest/ws/ingest" "WebSocket (400/401 expected)"

echo ""
echo "=========================================="
echo "Summary:"
echo "  Total:   $count"
echo "  Success: $success"
echo "  Errors:  $errors"
echo "=========================================="

rm -f "$COOKIE_JAR"

if [ $errors -eq 0 ]; then
    echo "✅ All routes passed!"
    exit 0
else
    echo "❌ Found $errors error(s)!"
    exit 1
fi
