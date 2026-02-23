package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// CrawlResult tracks the result of crawling a URL
type CrawlResult struct {
	URL        string
	StatusCode int
	Error      error
	Depth      int
}

var (
	baseURL  string
	username string
	password string
	maxDepth int
	visited  = make(map[string]bool)
	results  []CrawlResult
	client   *http.Client
)

func main() {
	flag.StringVar(&baseURL, "url", "http://localhost:18081", "Base URL to crawl")
	flag.StringVar(&username, "username", "demo", "Username for login")
	flag.StringVar(&password, "password", "demo123", "Password for login")
	flag.IntVar(&maxDepth, "depth", 5, "Maximum crawl depth")
	flag.Parse()

	jar, err := cookiejar.New(nil)
	if err != nil {
		fmt.Printf("Error creating cookie jar: %v\n", err)
		os.Exit(1)
	}

	client = &http.Client{
		Timeout: 10 * time.Second,
		Jar:     jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse // Don't auto-follow, track manually
		},
	}

	fmt.Println("=== Web Crawler ===")
	fmt.Printf("Base URL: %s\n", baseURL)
	fmt.Printf("Max Depth: %d\n", maxDepth)
	fmt.Println()

	// Login first
	if err := login(); err != nil {
		fmt.Printf("Login failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("✓ Logged in successfully")
	fmt.Println()

	// Start DFS crawl from root
	fmt.Println("=== Crawling ===")
	dfsCrawl("/", 0)

	// Print results
	fmt.Println()
	fmt.Println("=== Results ===")

	passed := 0
	failed := 0
	redirects := 0

	for _, result := range results {
		indent := strings.Repeat("  ", result.Depth)

		if result.Error != nil {
			fmt.Printf("%s❌ %s - ERROR: %v\n", indent, result.URL, result.Error)
			failed++
		} else if result.StatusCode == 500 {
			fmt.Printf("%s❌ %s - 500 Internal Server Error\n", indent, result.URL)
			failed++
		} else if result.StatusCode >= 300 && result.StatusCode < 400 {
			fmt.Printf("%s↪️  %s - %d\n", indent, result.URL, result.StatusCode)
			redirects++
		} else if result.StatusCode == 200 {
			fmt.Printf("%s✓ %s\n", indent, result.URL)
			passed++
		} else {
			fmt.Printf("%s⚠️  %s - %d\n", indent, result.URL, result.StatusCode)
			passed++ // Count as passed if not 500
		}
	}

	fmt.Println()
	fmt.Println("=== Summary ===")
	fmt.Printf("Total URLs: %d\n", len(results))
	fmt.Printf("✓ OK (200): %d\n", passed)
	fmt.Printf("↪️  Redirects: %d\n", redirects)
	fmt.Printf("❌ Failed: %d\n", failed)

	if failed > 0 {
		os.Exit(1)
	}
}

// DFS crawl starting from given path
func dfsCrawl(path string, depth int) {
	// Check depth limit
	if depth > maxDepth {
		return
	}

	// Skip if already visited
	if visited[path] {
		return
	}
	visited[path] = true

	// Skip static files and non-HTML resources
	if shouldSkip(path) {
		return
	}

	// Fetch the page
	resp, body, err := fetch(path)

	// Record result
	result := CrawlResult{
		URL:   path,
		Depth: depth,
	}
	if err != nil {
		result.Error = err
	} else {
		result.StatusCode = resp.StatusCode
	}
	results = append(results, result)

	// Only extract links from successful HTML responses
	if err != nil || resp.StatusCode != 200 {
		return
	}

	// Extract links and crawl them (DFS)
	links := extractLinksFromHTML(string(body))
	for _, link := range links {
		cleanLink := normalizeURL(link)
		if cleanLink != "" && !visited[cleanLink] {
			dfsCrawl(cleanLink, depth+1)
		}
	}
}

// extractLinksFromHTML parses HTML and extracts all href attributes from anchor tags
func extractLinksFromHTML(htmlStr string) []string {
	doc, err := html.Parse(strings.NewReader(htmlStr))
	if err != nil {
		return nil
	}

	var links []string
	seen := make(map[string]bool)

	var traverse func(*html.Node)
	traverse = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			for _, attr := range n.Attr {
				if attr.Key == "href" {
					link := attr.Val
					if !seen[link] {
						seen[link] = true
						links = append(links, link)
					}
					break
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			traverse(c)
		}
	}

	traverse(doc)
	return links
}

// fetch performs HTTP GET and returns response + body
func fetch(path string) (*http.Response, []byte, error) {
	req, err := http.NewRequest("GET", baseURL+path, nil)
	if err != nil {
		return nil, nil, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp, nil, err
	}

	return resp, body, nil
}

// normalizeURL cleans and validates a URL
func normalizeURL(raw string) string {
	// Parse the URL
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}

	// Skip external URLs (have scheme or host that's not ours)
	if u.Scheme != "" && !strings.HasPrefix(raw, baseURL) {
		return ""
	}

	// Parse base URL to compare hosts
	base, _ := url.Parse(baseURL)
	if u.Host != "" && u.Host != base.Host {
		return ""
	}

	// Must be absolute path (relative to same host)
	if !strings.HasPrefix(u.Path, "/") {
		return ""
	}

	// Remove query strings and fragments for crawling
	return u.Path
}

// shouldSkip returns true for static files and external URLs
func shouldSkip(path string) bool {
	// Skip common static file extensions
	staticExts := []string{
		".css", ".js", ".png", ".jpg", ".jpeg", ".gif", ".svg", ".ico",
		".woff", ".woff2", ".ttf", ".eot", ".pdf", ".zip", ".tar.gz",
	}
	lower := strings.ToLower(path)
	for _, ext := range staticExts {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}

	// Skip common non-page paths
	skipPrefixes := []string{
		"/assets/", "/static/", "/images/", "/fonts/",
		"mailto:", "tel:", "javascript:", "#",
	}
	for _, prefix := range skipPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}

	return false
}

// login authenticates and stores cookies in the jar
func login() error {
	form := url.Values{}
	form.Set("username", username)
	form.Set("password", password)

	req, err := http.NewRequest("POST", baseURL+"/login", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Accept 200 (HX-Redirect) or 302 (actual redirect)
	if resp.StatusCode != 200 && resp.StatusCode != 302 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	return nil
}
