#!/bin/bash
# Crawler script to test all panel routes

BASE_URL="http://localhost:18081"
COOKIE_JAR="/tmp/crawl-cookies.txt"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Counters
TOTAL=0
SUCCESS=0
FAILED=0
REDIRECTS=0

check_route() {
    local method=$1
    local path=$2
    local expected=$3
    local needs_auth=$4
    
    TOTAL=$((TOTAL + 1))
    
    if [ "$method" = "POST" ]; then
        # For POST, we just check it doesn't 500 (will likely redirect or 403)
        STATUS=$(curl -s -o /dev/null -w "%{http_code}" -X POST -b "$COOKIE_JAR" "$BASE_URL$path" 2>/dev/null)
    else
        STATUS=$(curl -s -o /dev/null -w "%{http_code}" -b "$COOKIE_JAR" "$BASE_URL$path" 2>/dev/null)
    fi
    
    # Check for 500 errors (our main concern)
    if [ "$STATUS" = "500" ]; then
        echo -e "${RED}FAIL${NC}: $method $path -> $STATUS (INTERNAL SERVER ERROR)"
        FAILED=$((FAILED + 1))
    elif [ "$STATUS" = "000" ]; then
        echo -e "${RED}FAIL${NC}: $method $path -> Connection error"
        FAILED=$((FAILED + 1))
    elif [ "$needs_auth" = "true" ] && [ "$STATUS" = "302" ]; then
        echo -e "${YELLOW}AUTH${NC}: $method $path -> $STATUS (redirect to login, expected)"
        REDIRECTS=$((REDIRECTS + 1))
    elif [ "$STATUS" = "$expected" ]; then
        echo -e "${GREEN}OK${NC}: $method $path -> $STATUS"
        SUCCESS=$((SUCCESS + 1))
    else
        echo -e "${YELLOW}INFO${NC}: $method $path -> $STATUS (expected $expected)"
        SUCCESS=$((SUCCESS + 1))
    fi
}

echo "========================================"
echo "Panel Routes Crawler"
echo "Base URL: $BASE_URL"
echo "========================================"
echo ""

# Public routes (no auth needed)
echo "--- Public Routes ---"
check_route "GET" "/" "200" "false"
check_route "GET" "/login" "200" "false"
check_route "GET" "/setup" "200" "false"
check_route "GET" "/llm" "200" "false"

# Ingest routes (no auth needed, API endpoints)
echo ""
echo "--- Ingest Routes ---"
check_route "GET" "/ingest/collectors" "401" "false"

# Panel routes (need auth, will redirect to login)
echo ""
echo "--- Panel Routes (need auth) ---"
check_route "GET" "/panel/" "302" "true"
check_route "GET" "/panel/collectors" "302" "true"
check_route "GET" "/panel/collectors/demo-alpha" "302" "true"
check_route "GET" "/panel/collectors/demo-alpha/resources" "302" "true"
check_route "GET" "/panel/collectors/demo-alpha/apps" "302" "true"
check_route "GET" "/panel/collectors/demo-alpha/apps/alpha-json" "302" "true"
check_route "GET" "/panel/collectors/demo-alpha/apps/alpha-json/logs" "302" "true"
check_route "GET" "/panel/collectors/demo-alpha/apps/alpha-json/logs/groups/1" "302" "true"
check_route "GET" "/panel/collectors/demo-alpha/apps/alpha-json/logs/1" "302" "true"

# Settings routes (need auth)
echo ""
echo "--- Settings Routes (need auth) ---"
check_route "GET" "/settings/" "302" "true"
check_route "GET" "/settings/entries" "302" "true"
check_route "GET" "/settings/users" "302" "true"
check_route "GET" "/settings/users/1/apps" "302" "true"
check_route "GET" "/settings/users/1/apps/entries" "302" "true"
check_route "GET" "/settings/api-key" "302" "true"
check_route "GET" "/settings/notifiers" "302" "true"
check_route "GET" "/settings/notifiers/1" "302" "true"
check_route "GET" "/settings/notifiers/1/entries" "302" "true"
check_route "GET" "/settings/notifiers/1/apps" "302" "true"
check_route "GET" "/settings/notifiers/1/apps/entries" "302" "true"

echo ""
echo "========================================"
echo "Results:"
echo "  Total:    $TOTAL"
echo -e "  Success:  ${GREEN}$SUCCESS${NC}"
echo -e "  Auth redirects: ${YELLOW}$REDIRECTS${NC}"
echo -e "  Failed:   ${RED}$FAILED${NC}"
echo "========================================"

if [ $FAILED -eq 0 ]; then
    echo -e "${GREEN}All routes passed! No 500 errors found.${NC}"
    exit 0
else
    echo -e "${RED}Found $FAILED route(s) with errors!${NC}"
    exit 1
fi
