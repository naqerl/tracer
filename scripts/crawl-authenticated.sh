#!/bin/bash
# Depth-First Search crawler for panel routes
# Discovers all URLs dynamically by following anchor links

BASE_URL="http://localhost:18081"
COOKIE_JAR="/tmp/crawl-auth-cookies.txt"
USERNAME="demo"
PASSWORD="demo123"

# Track visited and found URLs
declare -A visited
declare -a stack
count=0
errors=0
success=0
skipped=0

# URL patterns to skip (external, static files, etc.)
SKIP_PATTERNS="^(mailto:|tel:|javascript:|#|http://|https://|/.*\\.(css|js|png|jpg|jpeg|gif|svg|ico|woff|woff2|ttf|eot)$)"

# Extract links from HTML using grep/sed
extract_links() {
    local html="$1"
    local base_path="$2"
    
    # Extract href attributes from anchor tags
    echo "$html" | grep -oE 'href="[^"]+"' | sed 's/href="//;s/"$//' | while read -r link; do
        # Skip empty links
        [ -z "$link" ] && continue
        
        # Skip patterns
        if echo "$link" | grep -qE "$SKIP_PATTERNS"; then
            continue
        fi
        
        # Convert relative to absolute
        if [[ "$link" == /* ]]; then
            echo "$link"
        elif [[ "$link" == ./* ]]; then
            # Relative to current path
            echo "${base_path%/*}${link#.}"
        elif [[ "$link" == ../* ]]; then
            # Parent directory (simplified)
            echo "${base_path%/*}/$link"
        else
            # Same directory
            echo "${base_path%/*}/$link"
        fi
    done | sort -u
}

# Check a single URL
check_url() {
    local url="$1"
    local depth="$2"
    
    # Skip if already visited
    if [ -n "${visited[$url]}" ]; then
        return
    fi
    
    visited[$url]=1
    count=$((count + 1))
    
    # Fetch the page
    local response
    response=$(curl -s -w "\n---HTTP_CODE---\n%{http_code}" -b "$COOKIE_JAR" "$BASE_URL$url" 2>/dev/null)
    
    local http_code
    http_code=$(echo "$response" | grep -A1 "---HTTP_CODE---" | tail -1)
    local html
    html=$(echo "$response" | sed '/---HTTP_CODE---/,$d')
    
    local indent
    indent=$(printf '%*s' "$((depth * 2))" '')
    
    if [ "$http_code" = "500" ]; then
        echo "${indent}❌ FAIL: $url -> 500 ERROR"
        errors=$((errors + 1))
    elif [ "$http_code" = "000" ]; then
        echo "${indent}❌ FAIL: $url -> Connection error"
        errors=$((errors + 1))
    elif [ "$http_code" = "200" ]; then
        echo "${indent}✅ OK: $url"
        success=$((success + 1))
        
        # Extract and queue new links (DFS)
        local links
        links=$(extract_links "$html" "$url")
        
        for link in $links; do
            if [ -z "${visited[$link]}" ]; then
                # Push to stack for DFS
                stack+=("$link:$((depth + 1))")
            fi
        done
    elif [ "$http_code" = "302" ] || [ "$http_code" = "303" ] || [ "$http_code" = "301" ]; then
        echo "${indent}↪️  REDIRECT: $url -> $http_code"
        success=$((success + 1))
    elif [ "$http_code" = "401" ] || [ "$http_code" = "403" ]; then
        echo "${indent}🔒 AUTH: $url -> $http_code"
        skipped=$((skipped + 1))
    elif [ "$http_code" = "404" ]; then
        echo "${indent}⚠️  NOT FOUND: $url -> 404"
        errors=$((errors + 1))
    else
        echo "${indent}⚠️  INFO: $url -> $http_code"
        success=$((success + 1))
    fi
}

# DFS traversal
dfs_crawl() {
    while [ ${#stack[@]} -gt 0 ]; do
        # Pop from stack (DFS)
        local item="${stack[-1]}"
        unset 'stack[-1]'
        
        local url="${item%%:*}"
        local depth="${item##*:}"
        
        # Max depth to prevent infinite loops
        if [ "$depth" -gt 5 ]; then
            continue
        fi
        
        check_url "$url" "$depth"
    done
}

echo "=========================================="
echo "DFS Web Crawler for Panel"
echo "=========================================="
echo ""

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

echo ""
echo "Starting DFS crawl from /panel/..."
echo ""

# Seed the stack with starting URLs
stack=("/panel/:0")

# Run DFS
dfs_crawl

echo ""
echo "=========================================="
echo "Summary:"
echo "  Total visited: $count"
echo "  Success:       $success"
echo "  Errors:        $errors"
echo "  Skipped:       $skipped"
echo "=========================================="

rm -f "$COOKIE_JAR"

if [ $errors -eq 0 ]; then
    echo "✅ All routes passed!"
    exit 0
else
    echo "❌ Found $errors error(s)!"
    exit 1
fi
