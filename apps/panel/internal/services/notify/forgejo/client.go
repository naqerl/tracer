package forgejo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	listLimit      = 50
	maxPages       = 20      // 1000 items max; repos/orgs with more are truncated silently
	maxBody        = 4 << 20 // 4 MiB cap per list page
	maxErrorBody   = 1 << 10 // 1 KiB cap for remote bodies surfaced in errors
	minTokenLength = 5
)

// ErrUnauthorized is returned when Forgejo rejects the credentials (401/403).
var ErrUnauthorized = errors.New("invalid Forgejo credentials")

// responseError converts a non-2xx Forgejo response into an error. 401/403
// map to ErrUnauthorized with a short message so the remote body — which may
// echo the token — is never surfaced. Other statuses include a truncated,
// single-line snippet of the remote body so internal hosts cannot be probed
// for large or structured responses.
func responseError(prefix string, resp *http.Response, body []byte) error {
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("%w (HTTP %d)", ErrUnauthorized, resp.StatusCode)
	}
	snippet := strings.Join(strings.Fields(string(body)), " ")
	if len(snippet) > maxErrorBody {
		snippet = snippet[:maxErrorBody] + "…"
	}
	if snippet == "" {
		snippet = "empty response body"
	}
	return fmt.Errorf("%s returned %s: %s", prefix, resp.Status, snippet)
}

// Label is a repository label selectable for created issues.
type Label struct {
	ID   int64
	Name string
}

// Milestone is an open repository milestone selectable for created issues.
type Milestone struct {
	ID    int64
	Title string
}

// Owner is a repository owner the token can see (the user itself or one of
// its organizations).
type Owner struct {
	Name  string
	IsOrg bool
}

// Client talks to a Forgejo instance's REST API. The client is stateless
// between requests and safe for concurrent use; callers may share one client
// across goroutines.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// NewClient creates a client for the given instance and access token.
func NewClient(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 10 * time.Second},
	}
}

// ValidCredentials checks whether baseURL looks like a reachable HTTP(S) URL
// and the token meets Forgejo's minimum length.  It does not make network
// requests.
func ValidCredentials(baseURL, token string) error {
	if baseURL == "" {
		return fmt.Errorf("base URL is required")
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return fmt.Errorf("could not parse base URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("base URL must use http or https scheme")
	}
	if u.Host == "" {
		return fmt.Errorf("base URL has no host")
	}
	if u.User != nil {
		return fmt.Errorf("base URL must not contain userinfo")
	}
	if len(token) < minTokenLength {
		return fmt.Errorf("token is too short (minimum %d characters)", minTokenLength)
	}
	return nil
}

// ValidateOwner checks an owner name against Forgejo's name rules.
func ValidateOwner(owner string) error {
	if !ownerRepoPattern.MatchString(owner) {
		return fmt.Errorf("owner must match [A-Za-z0-9_.-]")
	}
	return nil
}

// ValidateOwnerRepo checks owner and repo against Forgejo's name rules.
func ValidateOwnerRepo(owner, repo string) error {
	if err := ValidateOwner(owner); err != nil {
		return err
	}
	if !ownerRepoPattern.MatchString(repo) {
		return fmt.Errorf("repo must match [A-Za-z0-9_.-]")
	}
	return nil
}

// ListOwners returns the authenticated user's login plus its organizations,
// following pages.  Errors listing organizations degrade to the user login
// alone.
func (c *Client) ListOwners(ctx context.Context) ([]Owner, error) {
	var owners []Owner

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/v1/user", nil)
	if err != nil {
		return nil, fmt.Errorf("could not build forgejo user request: %w", err)
	}
	c.authorize(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not fetch forgejo user: %w", err)
	}
	body, err := readBody(resp)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, responseError("forgejo user request", resp, body)
	}

	var u struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(body, &u); err != nil {
		return nil, fmt.Errorf("could not parse forgejo user: %w", err)
	}
	if u.Login != "" {
		owners = append(owners, Owner{Name: u.Login})
	}

	_, err = c.listPages(ctx, func(page int) string {
		return fmt.Sprintf("/api/v1/user/orgs?page=%d&limit=%d", page, listLimit)
	}, func(body []byte) (int, error) {
		var page []struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return 0, err
		}
		for _, o := range page {
			if o.Name != "" {
				owners = append(owners, Owner{Name: o.Name, IsOrg: true})
			}
		}
		return len(page), nil
	})
	if err != nil {
		// Tolerate org fetch errors: return the user login we already have.
		slog.Debug("could not list forgejo orgs, degrading to user login", "error", err)
		return owners, nil
	}

	return owners, nil
}

// ListRepos returns the repository names owned by the given owner.  User
// owners are queried first; a 404 falls back to the organization endpoint.
func (c *Client) ListRepos(ctx context.Context, owner string) ([]string, error) {
	parse := func(repos *[]string) func([]byte) (int, error) {
		return func(body []byte) (int, error) {
			var page []struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(body, &page); err != nil {
				return 0, err
			}
			for _, r := range page {
				if r.Name != "" {
					*repos = append(*repos, r.Name)
				}
			}
			return len(page), nil
		}
	}

	var repos []string
	status, err := c.listPages(ctx, func(page int) string {
		return fmt.Sprintf("/api/v1/users/%s/repos?page=%d&limit=%d", owner, page, listLimit)
	}, parse(&repos))
	if err == nil {
		return repos, nil
	}
	if status == http.StatusNotFound {
		repos = nil
		_, err = c.listPages(ctx, func(page int) string {
			return fmt.Sprintf("/api/v1/orgs/%s/repos?page=%d&limit=%d", owner, page, listLimit)
		}, parse(&repos))
		if err == nil {
			return repos, nil
		}
		return nil, fmt.Errorf("could not list repos for %s: %w", owner, err)
	}
	return nil, fmt.Errorf("could not list repos for %s: %w", owner, err)
}

// ListOwnerRepos returns "owner/repo" pairs for the authenticated user and
// every visible organization, combining ListOwners with ListRepos. Owners
// whose repository listing fails are skipped.
func (c *Client) ListOwnerRepos(ctx context.Context) ([]string, error) {
	owners, err := c.ListOwners(ctx)
	if err != nil {
		return nil, err
	}
	var pairs []string
	for _, o := range owners {
		repos, err := c.ListRepos(ctx, o.Name)
		if err != nil {
			slog.Debug("could not list forgejo repos for owner, skipping", "owner", o.Name, "error", err)
			continue
		}
		for _, repo := range repos {
			pairs = append(pairs, o.Name+"/"+repo)
		}
	}
	return pairs, nil
}

// ValidateRepo confirms the token can read the given repository.
func (c *Client) ValidateRepo(ctx context.Context, owner, repo string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+fmt.Sprintf("/api/v1/repos/%s/%s", owner, repo), nil)
	if err != nil {
		return fmt.Errorf("could not build forgejo repo request: %w", err)
	}
	c.authorize(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach forgejo: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, err := readBody(resp)
		if err != nil {
			return fmt.Errorf("could not read forgejo error response: %w", err)
		}
		return responseError("forgejo repo validation", resp, body)
	}

	return nil
}

// ListLabels returns the repository's labels, following pages.
func (c *Client) ListLabels(ctx context.Context, owner, repo string) ([]Label, error) {
	var labels []Label
	if _, err := c.listPages(ctx, func(page int) string {
		return fmt.Sprintf("/api/v1/repos/%s/%s/labels?page=%d&limit=%d", owner, repo, page, listLimit)
	}, func(body []byte) (int, error) {
		var page []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return 0, err
		}
		for _, l := range page {
			labels = append(labels, Label{ID: l.ID, Name: l.Name})
		}
		return len(page), nil
	}); err != nil {
		return nil, err
	}
	return labels, nil
}

// ListMilestones returns the repository's open milestones, following pages.
func (c *Client) ListMilestones(ctx context.Context, owner, repo string) ([]Milestone, error) {
	var milestones []Milestone
	if _, err := c.listPages(ctx, func(page int) string {
		return fmt.Sprintf("/api/v1/repos/%s/%s/milestones?state=open&page=%d&limit=%d", owner, repo, page, listLimit)
	}, func(body []byte) (int, error) {
		var page []struct {
			ID    int64  `json:"id"`
			Title string `json:"title"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return 0, err
		}
		for _, m := range page {
			milestones = append(milestones, Milestone{ID: m.ID, Title: m.Title})
		}
		return len(page), nil
	}); err != nil {
		return nil, err
	}
	return milestones, nil
}

// SearchIssue looks for an issue in owner/repo (any state) whose title
// contains query. It returns the first match, or ok=false when none exists.
// The search endpoint is global, so matches are additionally filtered
// client-side to the configured repo and to issues whose title actually
// contains the query.
func (c *Client) SearchIssue(ctx context.Context, owner, repo, query string) (Issue, bool, error) {
	// The first-line query is quoted so multi-word titles match as a single
	// term instead of an implicit AND of words; the limit is generous so the
	// client-side repo/title filter sees the full result set.
	u := c.baseURL + "/api/v1/repos/issues/search?" + url.Values{
		"q":     {fmt.Sprintf("repo:%s/%s \"%s\"", owner, repo, query)},
		"limit": {"100"},
	}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Issue{}, false, fmt.Errorf("could not build forgejo issue search request: %w", err)
	}
	c.authorize(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return Issue{}, false, fmt.Errorf("could not search forgejo issues: %w", err)
	}
	body, err := readBody(resp)
	if err != nil {
		return Issue{}, false, err
	}
	if resp.StatusCode != http.StatusOK {
		return Issue{}, false, responseError("forgejo issue search", resp, body)
	}

	var results []struct {
		Number     int64  `json:"number"`
		HTMLURL    string `json:"html_url"`
		Title      string `json:"title"`
		State      string `json:"state"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(body, &results); err != nil {
		return Issue{}, false, fmt.Errorf("could not decode forgejo issue search response: %w", err)
	}

	full := owner + "/" + repo
	for _, r := range results {
		if r.Repository.FullName != full {
			continue
		}
		if !strings.Contains(r.Title, query) {
			continue
		}
		return Issue{Number: r.Number, HTMLURL: r.HTMLURL, State: r.State}, true, nil
	}
	return Issue{}, false, nil
}

// CreateComment adds a comment to an issue, used to note that a reported error
// re-occurred on an existing (possibly closed) issue.
func (c *Client) CreateComment(ctx context.Context, owner, repo string, number int64, body string) error {
	payload, err := json.Marshal(map[string]string{"body": body})
	if err != nil {
		return fmt.Errorf("could not marshal comment body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+fmt.Sprintf("/api/v1/repos/%s/%s/issues/%d/comments", owner, repo, number), bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("could not build forgejo comment request: %w", err)
	}
	c.authorize(req)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("could not create forgejo comment: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		b, err := readBody(resp)
		if err != nil {
			return fmt.Errorf("forgejo comment creation returned %s", resp.Status)
		}
		return responseError("forgejo comment creation", resp, b)
	}
	return nil
}

// CreateIssue creates an issue and returns the created issue.
func (c *Client) CreateIssue(ctx context.Context, owner, repo, title, body string, labels []int64, milestone int64) (Issue, error) {
	payload, err := json.Marshal(createIssueOption{Title: title, Body: body, Labels: labels, Milestone: milestone})
	if err != nil {
		return Issue{}, fmt.Errorf("could not marshal issue body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+fmt.Sprintf("/api/v1/repos/%s/%s/issues", owner, repo), bytes.NewReader(payload))
	if err != nil {
		return Issue{}, fmt.Errorf("could not build forgejo issue request: %w", err)
	}
	c.authorize(req)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return Issue{}, fmt.Errorf("could not create forgejo issue: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		respBody, err := readBody(resp)
		if err != nil {
			return Issue{}, fmt.Errorf("forgejo issue creation returned %s", resp.Status)
		}
		return Issue{}, responseError("forgejo issue creation", resp, respBody)
	}

	respBody, err := readBody(resp)
	if err != nil {
		return Issue{}, fmt.Errorf("could not read forgejo issue response: %w", err)
	}

	var issue Issue
	if err := json.Unmarshal(respBody, &issue); err != nil {
		return Issue{}, fmt.Errorf("could not decode forgejo issue response: %w", err)
	}

	return issue, nil
}

// listPages GETs a paginated endpoint, following pages until an empty page or
// maxPages, and feeds each page's body to parse.  It returns the HTTP status
// of the last response so callers can distinguish a 404 from other failures.
func (c *Client) listPages(ctx context.Context, path func(page int) string, parse func([]byte) (int, error)) (int, error) {
	status := http.StatusOK
	for page := 1; page <= maxPages; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path(page), nil)
		if err != nil {
			return status, fmt.Errorf("could not build forgejo list request: %w", err)
		}
		c.authorize(req)

		resp, err := c.http.Do(req)
		if err != nil {
			return status, fmt.Errorf("could not fetch forgejo list: %w", err)
		}
		body, err := readBody(resp)
		if err != nil {
			return 0, err
		}

		status = resp.StatusCode
		if resp.StatusCode != http.StatusOK {
			return status, responseError("forgejo list", resp, body)
		}

		n, err := parse(body)
		if err != nil {
			return status, fmt.Errorf("could not parse forgejo list: %w", err)
		}
		if n < listLimit {
			return status, nil
		}
	}
	return status, nil
}

func (c *Client) authorize(req *http.Request) {
	req.Header.Set("Authorization", "token "+c.token)
}

// readBody reads and closes the response body, enforcing the maxBody cap.
// A body that fills the cap is treated as truncated and rejected.
func readBody(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, fmt.Errorf("could not read forgejo response body: %w", err)
	}
	if len(body) > maxBody {
		return nil, fmt.Errorf("forgejo response body exceeds %d bytes", maxBody)
	}
	return body, nil
}
