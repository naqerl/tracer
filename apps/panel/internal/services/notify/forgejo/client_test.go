package forgejo

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidateRepoOK(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"full_name":"x/barse"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "sekret")
	if err := c.ValidateRepo(t.Context(), "x", "barse"); err != nil {
		t.Fatalf("ValidateRepo: %v", err)
	}
	if gotPath != "/api/v1/repos/x/barse" {
		t.Errorf("expected repo path, got %q", gotPath)
	}
	if gotAuth != "token sekret" {
		t.Errorf("expected token auth, got %q", gotAuth)
	}
}

func TestValidateRepoUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "bad")
	err := c.ValidateRepo(t.Context(), "x", "barse")
	if err == nil {
		t.Fatal("expected error for unauthorized repo")
	}
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
}

func TestListLabelsFollowsPages(t *testing.T) {
	var pages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages = append(pages, r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "1" {
			items := make([]map[string]any, listLimit)
			for i := range items {
				items[i] = map[string]any{"id": i + 1, "name": fmt.Sprintf("label-%d", i+1)}
			}
			_ = json.NewEncoder(w).Encode(items)
			return
		}
		_, _ = w.Write([]byte(`[{"id":51,"name":"label-51"},{"id":52,"name":"label-52"},{"id":53,"name":"label-53"}]`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "t")
	labels, err := c.ListLabels(t.Context(), "x", "barse")
	if err != nil {
		t.Fatalf("ListLabels: %v", err)
	}
	if len(labels) != listLimit+3 {
		t.Fatalf("expected %d labels across pages, got %d", listLimit+3, len(labels))
	}
	if labels[0].ID != 1 || labels[0].Name != "label-1" {
		t.Errorf("unexpected first label: %+v", labels[0])
	}
	if labels[len(labels)-1].ID != 53 {
		t.Errorf("unexpected last label: %+v", labels[len(labels)-1])
	}
	if len(pages) != 2 {
		t.Fatalf("expected 2 page requests, got %d", len(pages))
	}
	for _, q := range pages {
		if !strings.Contains(q, "limit=50") {
			t.Errorf("expected limit=50 in query, got %q", q)
		}
	}
}

func TestListMilestonesOnlyOpen(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":7,"title":"v2.0"}]`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "t")
	milestones, err := c.ListMilestones(t.Context(), "x", "barse")
	if err != nil {
		t.Fatalf("ListMilestones: %v", err)
	}
	if len(milestones) != 1 || milestones[0].ID != 7 || milestones[0].Title != "v2.0" {
		t.Fatalf("unexpected milestones: %+v", milestones)
	}
	if !strings.Contains(gotQuery, "state=open") {
		t.Errorf("expected state=open in query, got %q", gotQuery)
	}
}

func TestListLabelsNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "t")
	if _, err := c.ListLabels(t.Context(), "x", "barse"); err == nil {
		t.Fatal("expected error on non-2xx label list")
	}
}

func TestCreateIssueSendsLabelsAndMilestone(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/x/barse/issues" {
			t.Errorf("unexpected path: %q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"number":9,"html_url":"https://git.naqerl.com/x/barse/issues/9"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "t")
	issue, err := c.CreateIssue(t.Context(), "x", "barse", "title", "body", []int64{3, 5}, 7)
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if issue.Number != 9 || issue.HTMLURL != "https://git.naqerl.com/x/barse/issues/9" {
		t.Errorf("unexpected issue: %+v", issue)
	}

	labels, _ := gotBody["labels"].([]any)
	if len(labels) != 2 || int(labels[0].(float64)) != 3 || int(labels[1].(float64)) != 5 {
		t.Errorf("unexpected labels in body: %v", gotBody["labels"])
	}
	if int(gotBody["milestone"].(float64)) != 7 {
		t.Errorf("unexpected milestone in body: %v", gotBody["milestone"])
	}
}

func TestCreateIssueOmitsEmptyLabelsAndMilestone(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"number":1,"html_url":"https://git.naqerl.com/x/barse/issues/1"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "t")
	if _, err := c.CreateIssue(t.Context(), "x", "barse", "title", "body", nil, 0); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if _, present := gotBody["labels"]; present {
		t.Errorf("expected labels omitted, got %v", gotBody["labels"])
	}
	if _, present := gotBody["milestone"]; present {
		t.Errorf("expected milestone omitted, got %v", gotBody["milestone"])
	}
}

func TestListOwnerReposCombinesOwners(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/user":
			_, _ = w.Write([]byte(`{"login":"demo"}`))
		case "/api/v1/user/orgs":
			_, _ = w.Write([]byte(`[{"name":"naqerl"}]`))
		case "/api/v1/users/demo/repos":
			_, _ = w.Write([]byte(`[{"name":"barse"},{"name":"tracer"}]`))
		case "/api/v1/orgs/naqerl/repos":
			_, _ = w.Write([]byte(`[{"name":"website"}]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "t")
	pairs, err := c.ListOwnerRepos(t.Context())
	if err != nil {
		t.Fatalf("ListOwnerRepos: %v", err)
	}
	want := []string{"demo/barse", "demo/tracer", "naqerl/website"}
	if len(pairs) != len(want) {
		t.Fatalf("expected %d pairs, got %+v", len(want), pairs)
	}
	for i, w := range want {
		if pairs[i] != w {
			t.Errorf("expected %q at index %d, got %q", w, i, pairs[i])
		}
	}
}

func TestListOwnerReposSkipsOwnersWithoutRepos(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/user":
			_, _ = w.Write([]byte(`{"login":"demo"}`))
		case "/api/v1/user/orgs":
			_, _ = w.Write([]byte(`[{"name":"empty"}]`))
		case "/api/v1/users/demo/repos":
			_, _ = w.Write([]byte(`[{"name":"barse"}]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "t")
	pairs, err := c.ListOwnerRepos(t.Context())
	if err != nil {
		t.Fatalf("ListOwnerRepos: %v", err)
	}
	if len(pairs) != 1 || pairs[0] != "demo/barse" {
		t.Fatalf("expected only demo/barse, got %+v", pairs)
	}
}

func TestValidateOwnerRepo(t *testing.T) {
	if err := ValidateOwnerRepo("x", "barse"); err != nil {
		t.Errorf("valid owner/repo rejected: %v", err)
	}
	for _, bad := range []struct{ owner, repo string }{
		{"x/y", "barse"},
		{"x", "barse?q"},
		{"x", "ba rse"},
		{"", "barse"},
	} {
		if err := ValidateOwnerRepo(bad.owner, bad.repo); err == nil {
			t.Errorf("expected error for %q/%q", bad.owner, bad.repo)
		}
	}
}

func TestListOwners(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/user":
			_, _ = w.Write([]byte(`{"login":"demo"}`))
		case "/api/v1/user/orgs":
			_, _ = w.Write([]byte(`[{"name":"naqerl"},{"name":"eco"}]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "t")
	owners, err := c.ListOwners(t.Context())
	if err != nil {
		t.Fatalf("ListOwners: %v", err)
	}
	if len(owners) != 3 {
		t.Fatalf("expected 3 owners, got %+v", owners)
	}
	if owners[0].Name != "demo" || owners[0].IsOrg {
		t.Errorf("expected demo as non-org owner, got %+v", owners[0])
	}
	if owners[1].Name != "naqerl" || !owners[1].IsOrg {
		t.Errorf("expected naqerl as org owner, got %+v", owners[1])
	}
}

func TestListOwnersUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "bad")
	_, err := c.ListOwners(t.Context())
	if err == nil {
		t.Fatal("expected error for unauthorized user request")
	}
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
}

func TestListReposForUser(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"name":"barse"},{"name":"tracer"}]`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "t")
	repos, err := c.ListRepos(t.Context(), "x")
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if gotPath != "/api/v1/users/x/repos" {
		t.Errorf("expected user repos path, got %q", gotPath)
	}
	if len(repos) != 2 || repos[0] != "barse" || repos[1] != "tracer" {
		t.Errorf("unexpected repos: %v", repos)
	}
}

func TestListReposFallsBackToOrg(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/users/naqerl/repos":
			w.WriteHeader(http.StatusNotFound)
		case "/api/v1/orgs/naqerl/repos":
			_, _ = w.Write([]byte(`[{"name":"website"},{"name":"tracer"}]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "t")
	repos, err := c.ListRepos(t.Context(), "naqerl")
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(repos) != 2 || repos[0] != "website" {
		t.Errorf("unexpected org repos: %v", repos)
	}
	if len(paths) != 2 {
		t.Errorf("expected users then orgs request, got %v", paths)
	}
}

func TestListReposNotFoundForNeither(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "t")
	if _, err := c.ListRepos(t.Context(), "ghost"); err == nil {
		t.Fatal("expected error when owner has no repos on either endpoint")
	}
}

func TestValidCredentialsRejectsUserinfo(t *testing.T) {
	if err := ValidCredentials("https://user:pass@git.example.com", "tokensekret"); err == nil {
		t.Fatal("expected error for base URL with userinfo")
	}
}

func TestResponseErrorCapsBody(t *testing.T) {
	resp := &http.Response{StatusCode: http.StatusNotFound, Status: "404 Not Found"}
	big := strings.Repeat("A", 8<<10) // 8 KiB, over maxErrorBody
	err := responseError("forgejo list", resp, []byte(big))
	if err == nil {
		t.Fatal("expected error")
	}
	if len(err.Error()) > maxErrorBody+len("forgejo list returned 404 Not Found: ")+10 {
		t.Errorf("error message not capped, length %d: %.120s…", len(err.Error()), err.Error())
	}
}

func TestResponseErrorCollapsesWhitespaceAndShowsEmptyBody(t *testing.T) {
	resp := &http.Response{StatusCode: http.StatusBadGateway, Status: "502 Bad Gateway"}
	err := responseError("forgejo list", resp, []byte("line1\n\n  line2\twith   spaces\n"))
	if !strings.Contains(err.Error(), "line1 line2 with spaces") {
		t.Errorf("expected collapsed single-line snippet, got %v", err)
	}
	errEmpty := responseError("forgejo list", resp, []byte("   \n\t"))
	if !strings.Contains(errEmpty.Error(), "empty response body") {
		t.Errorf("expected empty body placeholder, got %v", errEmpty)
	}
}

func TestReadBodyRejectsOversizedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(make([]byte, maxBody+100))
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("get test server: %v", err)
	}
	if _, err := readBody(resp); err == nil {
		t.Fatal("expected error for oversized response body")
	}
}

func TestSearchIssueFindsMatchingOpenIssue(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/issues/search" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		gotQuery = r.URL.Query().Get("q")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"number":42,"html_url":"https://git.naqerl.com/x/barse/issues/42","title":"boom [deadbeef0]","state":"open","repository":{"full_name":"x/barse"}}]`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "sekret")
	issue, found, err := c.SearchIssue(t.Context(), "x", "barse", "deadbeef0")
	if err != nil {
		t.Fatalf("SearchIssue: %v", err)
	}
	if !found {
		t.Fatal("expected a match")
	}
	if issue.Number != 42 || issue.HTMLURL != "https://git.naqerl.com/x/barse/issues/42" {
		t.Errorf("unexpected issue: %+v", issue)
	}
	if !strings.Contains(gotQuery, "repo:x/barse") || !strings.Contains(gotQuery, "deadbeef0") {
		t.Errorf("unexpected search query %q", gotQuery)
	}
}

func TestSearchIssueFiltersOtherRepoAndTitleMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"number":1,"html_url":"https://git.naqerl.com/y/other/issues/1","title":"boom [deadbeef0]","state":"open","repository":{"full_name":"y/other"}},
			{"number":2,"html_url":"https://git.naqerl.com/x/barse/issues/2","title":"unrelated title","state":"open","repository":{"full_name":"x/barse"}}
		]`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "sekret")
	if _, found, err := c.SearchIssue(t.Context(), "x", "barse", "deadbeef0"); err != nil {
		t.Fatalf("SearchIssue: %v", err)
	} else if found {
		t.Fatal("expected no match when the repo or title does not match")
	}
}

func TestSearchIssueEmptyResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "sekret")
	if _, found, err := c.SearchIssue(t.Context(), "x", "barse", "deadbeef0"); err != nil {
		t.Fatalf("SearchIssue: %v", err)
	} else if found {
		t.Fatal("expected no match for empty results")
	}
}

func TestSearchIssueSearchesAllStates(t *testing.T) {
	var gotState string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotState = r.URL.Query().Get("state")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "sekret")
	if _, _, err := c.SearchIssue(t.Context(), "x", "barse", "deadbeef0"); err != nil {
		t.Fatalf("SearchIssue: %v", err)
	}
	if gotState != "" {
		t.Errorf("expected no state filter so closed issues are found too, got %q", gotState)
	}
}

func TestSearchIssueReturnsClosedState(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"number":7,"html_url":"https://git.naqerl.com/x/barse/issues/7","title":"boom [deadbeef0]","state":"closed","repository":{"full_name":"x/barse"}}]`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "sekret")
	issue, found, err := c.SearchIssue(t.Context(), "x", "barse", "deadbeef0")
	if err != nil {
		t.Fatalf("SearchIssue: %v", err)
	}
	if !found {
		t.Fatal("expected a match for a closed issue")
	}
	if issue.State != "closed" {
		t.Errorf("expected closed state on the issue, got %q", issue.State)
	}
}

func TestReopenIssue(t *testing.T) {
	var gotPath, gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		buf, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		gotBody = string(buf)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"number":7,"state":"open"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "sekret")
	if err := c.ReopenIssue(t.Context(), "x", "barse", 7); err != nil {
		t.Fatalf("ReopenIssue: %v", err)
	}
	if gotPath != "/api/v1/repos/x/barse/issues/7" {
		t.Errorf("unexpected path %q", gotPath)
	}
	if gotAuth != "token sekret" {
		t.Errorf("unexpected auth %q", gotAuth)
	}
	if !strings.Contains(gotBody, `"state":"open"`) {
		t.Errorf("expected state=open in reopen body, got %q", gotBody)
	}
}

func TestReopenIssueNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "sekret")
	if err := c.ReopenIssue(t.Context(), "x", "barse", 7); err == nil {
		t.Fatal("expected error for non-2xx reopen")
	}
}
