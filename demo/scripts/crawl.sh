#!/bin/bash
# Simple crawler to test panel endpoints

BASE_URL="http://localhost:18081"
COOKIE_JAR="/tmp/crawl-cookies.txt"

# Login
echo "Logging in..."
curl -s -c "$COOKIE_JAR" -X POST "$BASE_URL/login" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "username=demo&password=demo123" \
  -o /dev/null

if ! grep -q "tracer_session" "$COOKIE_JAR" 2>/dev/null; then
    echo "ERROR: Login failed"
    exit 1
fi
echo "OK: Logged in"

# Test endpoints
test_endpoint() {
    local url=$1
    local desc=$2
    local status=$(curl -s -o /dev/null -w "%{http_code}" -b "$COOKIE_JAR" "$BASE_URL$url")
    if [ "$status" = "200" ] || [ "$status" = "302" ]; then
        echo "OK: $url ($desc) - $status"
    else
        echo "FAIL: $url ($desc) - $status"
    fi
}

echo ""
echo "Testing endpoints..."
test_endpoint "/panel/" "Dashboard"
test_endpoint "/panel/collectors" "Collectors list"
test_endpoint "/settings/" "Settings"
test_endpoint "/settings/users" "Users"

# Cleanup
rm -f "$COOKIE_JAR"

echo ""
echo "Crawl complete"
