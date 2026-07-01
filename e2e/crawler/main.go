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

type CrawlResult struct {
	URL        string
	StatusCode int
	Error      error
	Depth      int
	Duration   time.Duration
}

var (
	baseURL   string
	username  string
	password  string
	startPath string
	maxDepth  int
	visited   = make(map[string]bool)
	results   []CrawlResult
	client    *http.Client
)

func main() {
	var noAuth bool
	flag.StringVar(&baseURL, "url", "http://localhost:18081", "Base URL to crawl")
	flag.StringVar(&username, "username", "demo", "Username for login")
	flag.StringVar(&password, "password", "demo1234", "Password for login")
	flag.StringVar(&startPath, "start", "/panel/", "Path to start crawling from")
	flag.IntVar(&maxDepth, "depth", 100, "Maximum crawl depth")
	flag.BoolVar(&noAuth, "no-auth", false, "Skip login (panel in no-auth mode)")
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
			return http.ErrUseLastResponse
		},
	}

	fmt.Println("=== Web Crawler ===")
	fmt.Printf("Base URL: %s\n", baseURL)
	fmt.Printf("Start Path: %s\n", startPath)
	fmt.Printf("Max Depth: %d\n", maxDepth)
	fmt.Println()

	if noAuth {
		fmt.Println("OK no-auth mode (skipping login)")
	} else if err := login(); err != nil {
		fmt.Printf("Login failed: %v\n", err)
		os.Exit(1)
	} else {
		fmt.Println("OK logged in successfully")
	}
	fmt.Println()

	fmt.Println("=== Crawling ===")
	dfsCrawl(startPath, 0)

	fmt.Println()
	fmt.Println("=== Results ===")

	passed := 0
	failed := 0
	redirects := 0

	for _, result := range results {
		indent := strings.Repeat("  ", result.Depth)

		switch {
		case result.Error != nil:
			fmt.Printf("%sFAIL %s - %s - ERROR: %v\n", indent, result.URL, formatDuration(result.Duration), result.Error)
			failed++
		case result.StatusCode == http.StatusInternalServerError:
			fmt.Printf("%sFAIL %s - %s - 500 Internal Server Error\n", indent, result.URL, formatDuration(result.Duration))
			failed++
		case result.StatusCode >= 300 && result.StatusCode < 400:
			fmt.Printf("%sREDIRECT %s - %s - %d\n", indent, result.URL, formatDuration(result.Duration), result.StatusCode)
			redirects++
		case result.StatusCode == http.StatusOK:
			fmt.Printf("%sOK %s - %s\n", indent, result.URL, formatDuration(result.Duration))
			passed++
		default:
			fmt.Printf("%sWARN %s - %s - %d\n", indent, result.URL, formatDuration(result.Duration), result.StatusCode)
			passed++
		}
	}

	fmt.Println()
	fmt.Println("=== Summary ===")
	fmt.Printf("Total URLs: %d\n", len(results))
	fmt.Printf("OK (200): %d\n", passed)
	fmt.Printf("Redirects: %d\n", redirects)
	fmt.Printf("Failed: %d\n", failed)

	if failed > 0 {
		os.Exit(1)
	}
}

func dfsCrawl(path string, depth int) {
	if depth > maxDepth || visited[path] {
		return
	}
	visited[path] = true

	if shouldSkip(path) {
		return
	}

	resp, body, duration, err := fetch(path)
	result := CrawlResult{URL: path, Depth: depth, Duration: duration}
	if err != nil {
		result.Error = err
	} else {
		result.StatusCode = resp.StatusCode
	}
	results = append(results, result)

	if err != nil || resp.StatusCode != http.StatusOK {
		return
	}

	for _, link := range extractLinksFromHTML(string(body)) {
		cleanLink := normalizeURL(link)
		if cleanLink != "" && !visited[cleanLink] {
			dfsCrawl(cleanLink, depth+1)
		}
	}
}

func extractLinksFromHTML(htmlStr string) []string {
	doc, err := html.Parse(strings.NewReader(htmlStr))
	if err != nil {
		return nil
	}

	var links []string
	seen := make(map[string]bool)

	var traverse func(*html.Node)
	traverse = func(n *html.Node) {
		if n.Type == html.ElementNode {
			for _, attr := range n.Attr {
				if !isNavigableAttr(attr.Key) || seen[attr.Val] {
					continue
				}
				if attr.Val == "" {
					continue
				}
				seen[attr.Val] = true
				links = append(links, attr.Val)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			traverse(c)
		}
	}

	traverse(doc)
	return links
}

func isNavigableAttr(key string) bool {
	return key == "href" || key == "hx-get"
}

func formatDuration(d time.Duration) string {
	if d < time.Millisecond {
		return fmt.Sprintf("%dus", d.Microseconds())
	}
	return fmt.Sprintf("%.2fms", float64(d)/float64(time.Millisecond))
}

func fetch(path string) (*http.Response, []byte, time.Duration, error) {
	req, err := http.NewRequest(http.MethodGet, baseURL+path, nil)
	if err != nil {
		return nil, nil, 0, err
	}

	startedAt := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, time.Since(startedAt), err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp, nil, time.Since(startedAt), err
	}

	return resp, body, time.Since(startedAt), nil
}

func normalizeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}

	if u.Scheme != "" && !strings.HasPrefix(raw, baseURL) {
		return ""
	}

	base, _ := url.Parse(baseURL)
	if u.Host != "" && u.Host != base.Host {
		return ""
	}

	if !strings.HasPrefix(u.Path, "/") {
		return ""
	}

	return u.Path
}

func shouldSkip(path string) bool {
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

func login() error {
	form := url.Values{}
	form.Set("username", username)
	form.Set("password", password)

	req, err := http.NewRequest(http.MethodPost, baseURL+"/auth/login", strings.NewReader(form.Encode()))
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

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusFound {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	return nil
}
