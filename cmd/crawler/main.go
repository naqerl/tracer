package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

type EndpointTest struct {
	URL        string
	Type       string // "html", "markdown", "ajax"
	StatusCode int
	Error      error
}

var (
	baseURL    string
	apiKey     string
	visited    = make(map[string]bool)
	results    []EndpointTest
	client     *http.Client
	// Extract links from markdown: [text](/path) or href="/path"
	linkRegex  = regexp.MustCompile(`(?:\[.*?\]\(|href=")(/[^)\s"]+)`)
)

func main() {
	flag.StringVar(&baseURL, "url", "https://tracer.aishift.co", "Base URL to crawl")
	flag.Parse()

	apiKey = os.Getenv("TRACER_API_KEY")
	if apiKey == "" {
		fmt.Println("Error: TRACER_API_KEY environment variable not set")
		os.Exit(1)
	}

	client = &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse // Don't follow redirects
		},
	}

	fmt.Println("Starting crawler...")
	fmt.Printf("Base URL: %s\n", baseURL)
	fmt.Println("---")

	// Start crawling from /analytics
	crawl("/analytics")

	// Test some additional known endpoints
	testEndpoint("/llm")

	// Print results
	fmt.Println("\n=== Test Results ===")

	passed := 0
	failed := 0

	for _, result := range results {
		status := "✓"
		if result.Error != nil || result.StatusCode >= 400 {
			status = "✗"
			failed++
		} else {
			passed++
		}

		if result.Error != nil {
			fmt.Printf("%s [%s] %s - ERROR: %v\n", status, result.Type, result.URL, result.Error)
		} else {
			fmt.Printf("%s [%s] %s - %d\n", status, result.Type, result.URL, result.StatusCode)
		}
	}

	fmt.Printf("\n=== Summary ===\n")
	fmt.Printf("Passed: %d\n", passed)
	fmt.Printf("Failed: %d\n", failed)
	fmt.Printf("Total: %d\n", passed+failed)

	if failed > 0 {
		os.Exit(1)
	}
}

func crawl(path string) {
	if visited[path] {
		return
	}
	visited[path] = true

	// Test this endpoint in all formats
	testEndpoint(path)

	// Get HTML content to extract links
	req, err := http.NewRequest("GET", baseURL+path+".md", nil)
	if err != nil {
		return
	}
	req.Header.Set("X-API-Key", apiKey)

	resp, err := client.Do(req)
	if err != nil || resp.StatusCode >= 400 {
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return
	}

	// Extract and crawl links from markdown
	matches := linkRegex.FindAllStringSubmatch(string(body), -1)
	for _, match := range matches {
		if len(match) > 1 {
			linkPath := match[1]

			// Only follow analytics and llm paths
			if !strings.HasPrefix(linkPath, "/analytics") && !strings.HasPrefix(linkPath, "/llm") {
				continue
			}

			// Remove .md suffix for crawling
			linkPath = strings.TrimSuffix(linkPath, ".md")

			// Parse to handle query params and fragments
			u, err := url.Parse(linkPath)
			if err == nil {
				cleanPath := u.Path
				if !visited[cleanPath] && cleanPath != "" {
					crawl(cleanPath)
				}
			}
		}
	}
}

func testEndpoint(path string) {
	// Test HTML
	testRequest(path, "html", false)

	// Test Markdown
	testRequest(path+".md", "markdown", false)

	// Test AJAX
	testRequest(path, "ajax", true)
}

func testRequest(path string, testType string, ajax bool) {
	req, err := http.NewRequest("GET", baseURL+path, nil)
	if err != nil {
		results = append(results, EndpointTest{
			URL:   path,
			Type:  testType,
			Error: err,
		})
		return
	}

	req.Header.Set("X-API-Key", apiKey)

	if ajax {
		req.Header.Set("HX-Request", "true")
	}

	resp, err := client.Do(req)
	if err != nil {
		results = append(results, EndpointTest{
			URL:   path,
			Type:  testType,
			Error: err,
		})
		return
	}
	defer resp.Body.Close()

	// Read body to ensure full response
	io.ReadAll(resp.Body)

	results = append(results, EndpointTest{
		URL:        path,
		Type:       testType,
		StatusCode: resp.StatusCode,
	})
}
