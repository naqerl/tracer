package forgejo

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"tracer/apps/panel/internal/services/notify"
)

// newTestNotifier builds a notifier against a fake server whose handler must
// answer the issue list (GET /api/v1/repos/x/barse/issues?state=all) and,
// when no match is returned, the create request (POST /api/v1/repos/x/barse/issues).
func newTestNotifier(t *testing.T, handler http.HandlerFunc) *Notifier {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	n, err := New("issues", `{"base_url":"`+srv.URL+`","token":"sekret","owner":"x","repo":"barse"}`, NewCache(time.Minute))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return n
}

func TestNewRejectsNilCache(t *testing.T) {
	if _, err := New("issues", `{"base_url":"https://example.invalid","token":"sekret","owner":"x","repo":"barse"}`, nil); err == nil {
		t.Fatal("expected error for nil cache")
	}
}

func alertNotification() notify.Notification {
	return notify.Notification{
		Type:          notify.TypeAlert,
		CollectorName: "prod",
		AppName:       "api",
		Payload: notify.LogEntry{
			Message: "connection refused\nsecond line",
			GroupID: 7,
			LogURL:  "https://panel.example.com/panel/collectors/prod/apps/api/logs/groups/7",
		},
	}
}

// isIssueListRequest reports whether r is a GET to the issues endpoint with
// state=all, i.e. the FindIssueByTitle list call.
func isIssueListRequest(r *http.Request) bool {
	return r.Method == http.MethodGet &&
		r.URL.Query().Get("state") == "all"
}

func TestNotifyListsThenCreatesIssue(t *testing.T) {
	var listCalls, createCalls int
	var createBody string
	n := newTestNotifier(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/repos/x/barse/issues" && isIssueListRequest(r):
			listCalls++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
		case r.URL.Path == "/api/v1/repos/x/barse/issues" && r.Method == http.MethodPost:
			createCalls++
			buf, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			createBody = string(buf)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"number": 42, "html_url": "https://git.naqerl.com/x/barse/issues/42"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	if err := n.Notify(alertNotification()); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	if listCalls != 1 || createCalls != 1 {
		t.Fatalf("expected 1 list and 1 create, got list=%d create=%d", listCalls, createCalls)
	}

	var body struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if err := json.Unmarshal([]byte(createBody), &body); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	if body.Title != "connection refused" {
		t.Errorf("unexpected title: %q", body.Title)
	}
	if !strings.Contains(body.Body, "https://panel.example.com/panel/collectors/prod/apps/api/logs/groups/7") {
		t.Errorf("expected group link in body, got: %q", body.Body)
	}
}

func TestNotifySkipsExistingIssue(t *testing.T) {
	var createCalls int
	n := newTestNotifier(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/repos/x/barse/issues" && isIssueListRequest(r):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"number":5,"html_url":"https://git.naqerl.com/x/barse/issues/5","title":"connection refused","state":"open"}]`))
		case r.URL.Path == "/api/v1/repos/x/barse/issues" && r.Method == http.MethodPost:
			createCalls++
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	if err := n.Notify(alertNotification()); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if createCalls != 0 {
		t.Fatalf("expected no issue creation when a match exists, got %d creates", createCalls)
	}
}

func TestNotifyCommentsOnClosedIssue(t *testing.T) {
	var createCalls, commentCalls int
	var commentPath, commentBody string
	n := newTestNotifier(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/repos/x/barse/issues" && isIssueListRequest(r):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"number":5,"html_url":"https://git.naqerl.com/x/barse/issues/5","title":"connection refused","state":"closed"}]`))
		case r.URL.Path == "/api/v1/repos/x/barse/issues/5/comments" && r.Method == http.MethodPost:
			commentCalls++
			commentPath = r.URL.Path
			var req struct {
				Body string `json:"body"`
			}
			buf, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			_ = json.Unmarshal(buf, &req)
			commentBody = req.Body
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":1}`))
		case r.URL.Path == "/api/v1/repos/x/barse/issues" && r.Method == http.MethodPost:
			createCalls++
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	if err := n.Notify(alertNotification()); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if createCalls != 0 {
		t.Fatalf("expected no issue creation when a closed match exists, got %d creates", createCalls)
	}
	if commentCalls != 1 || commentPath != "/api/v1/repos/x/barse/issues/5/comments" {
		t.Fatalf("expected a comment on the closed issue, got %d comments at %q", commentCalls, commentPath)
	}

	// The re-occurrence comment is a short pointer to the log, not the full report.
	if want := "This error re-occurred — [view log](https://panel.example.com/panel/collectors/prod/apps/api/logs/groups/7)."; commentBody != want {
		t.Errorf("unexpected comment body:\n got %q\nwant %q", commentBody, want)
	}

	// The closed issue is remembered: an immediate repeat skips entirely.
	if err := n.Notify(alertNotification()); err != nil {
		t.Fatalf("repeat alert: %v", err)
	}
	if commentCalls != 1 || createCalls != 0 {
		t.Fatalf("repeat alert must hit the cache, got comment=%d create=%d", commentCalls, createCalls)
	}
}

func TestNotifyIgnoresIssueFromOtherRepo(t *testing.T) {
	var createCalls int
	// The list-issues endpoint returns issues from this repo only; a match
	// from another repo would not appear here. This test verifies that a
	// non-matching title in the same repo does not prevent issue creation.
	n := newTestNotifier(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/repos/x/barse/issues" && isIssueListRequest(r):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"number":9,"html_url":"https://git.naqerl.com/x/barse/issues/9","title":"boom [deadbeef0]","state":"open"}]`))
		case r.URL.Path == "/api/v1/repos/x/barse/issues" && r.Method == http.MethodPost:
			createCalls++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"number":1,"html_url":"https://git.naqerl.com/x/barse/issues/1"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	if err := n.Notify(alertNotification()); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if createCalls != 1 {
		t.Fatalf("expected a create when no matching title exists, got %d", createCalls)
	}
}

func TestNotifyTruncatesLongTitle(t *testing.T) {
	var gotTitle string
	n := newTestNotifier(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/repos/x/barse/issues" && isIssueListRequest(r):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
		case r.URL.Path == "/api/v1/repos/x/barse/issues" && r.Method == http.MethodPost:
			var body struct {
				Title string `json:"title"`
			}
			buf, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			_ = json.Unmarshal(buf, &body)
			gotTitle = body.Title
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"number": 1, "html_url": "https://git.naqerl.com/x/barse/issues/1"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	// 200 multi-byte runes exceed Forgejo's 255-char title limit when counted
	// in bytes; the title must be cut to 255 bytes without splitting a rune.
	long := strings.Repeat("界", 200)
	if err := n.Notify(notify.Notification{
		Type:          notify.TypeAlert,
		CollectorName: "c",
		AppName:       "a",
		Payload: notify.LogEntry{
			Message: long,
			GroupID: 1,
		},
	}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if len(gotTitle) > 255 {
		t.Errorf("title exceeds 255 bytes, got %d: %q", len(gotTitle), gotTitle)
	}
	if !utf8.ValidString(gotTitle) {
		t.Errorf("title splits a UTF-8 sequence: %q", gotTitle)
	}
}

func TestNotifyIgnoresOtherTypes(t *testing.T) {
	n := newTestNotifier(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError) // fail loudly if called
	})

	for _, typ := range []notify.NotificationType{notify.TypeLifecycle, notify.TypeSubscription} {
		if err := n.Notify(notify.Notification{Type: typ}); err != nil {
			t.Fatalf("Notify(%s): %v", typ, err)
		}
	}
}

// TestNotifySkipsRecentlyReported covers the index-lag window: after a create,
// an identical alert must not hit the list or create endpoints again.
func TestNotifySkipsRecentlyReported(t *testing.T) {
	var listCalls, createCalls int
	n := newTestNotifier(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/repos/x/barse/issues" && isIssueListRequest(r):
			listCalls++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
		case r.URL.Path == "/api/v1/repos/x/barse/issues" && r.Method == http.MethodPost:
			createCalls++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"number":1,"html_url":"https://git.naqerl.com/x/barse/issues/1"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	if err := n.Notify(alertNotification()); err != nil {
		t.Fatalf("first alert: %v", err)
	}
	if listCalls != 1 || createCalls != 1 {
		t.Fatalf("expected 1 list + 1 create, got list=%d create=%d", listCalls, createCalls)
	}

	// Immediate repeat: cache hit, no network at all.
	if err := n.Notify(alertNotification()); err != nil {
		t.Fatalf("second alert: %v", err)
	}
	if listCalls != 1 || createCalls != 1 {
		t.Fatalf("repeat alert must be served from cache, got list=%d create=%d", listCalls, createCalls)
	}
}

// TestNotifyRetriesAfterCreateFailure ensures a failed create forgets the
// reservation so the next alert searches and creates again.
func TestNotifyRetriesAfterCreateFailure(t *testing.T) {
	var createCalls int
	n := newTestNotifier(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/repos/x/barse/issues" && isIssueListRequest(r):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
		case r.URL.Path == "/api/v1/repos/x/barse/issues" && r.Method == http.MethodPost:
			createCalls++
			http.Error(w, "boom", http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	if err := n.Notify(alertNotification()); err == nil {
		t.Fatal("expected error when create fails")
	}
	if createCalls != 1 {
		t.Fatalf("expected 1 create attempt, got %d", createCalls)
	}

	// Next alert must retry, not be swallowed by a stale reservation.
	if err := n.Notify(alertNotification()); err == nil {
		t.Fatal("expected error when create fails again")
	}
	if createCalls != 2 {
		t.Fatalf("expected a retry create after failure, got %d creates", createCalls)
	}
}

func TestWelcomeValidatesConnection(t *testing.T) {
	var gotPath string
	n := newTestNotifier(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"full_name":"x/barse"}`))
	})

	if err := n.Notify(notify.Notification{Type: notify.TypeWelcome}); err != nil {
		t.Fatalf("welcome: %v", err)
	}
	if gotPath != "/api/v1/repos/x/barse" {
		t.Errorf("expected repo validation path, got %q", gotPath)
	}
}

func TestWelcomeRejectsUnreachableRepo(t *testing.T) {
	n := newTestNotifier(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})

	err := n.Notify(notify.Notification{Type: notify.TypeWelcome})
	if err == nil {
		t.Fatal("expected error for unauthorized repo validation")
	}
	if !errors.Is(err, notify.ErrWelcomeValidation) {
		t.Fatalf("expected ErrWelcomeValidation, got %v", err)
	}
}

func TestNotifyNon2xxReturnsError(t *testing.T) {
	n := newTestNotifier(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/repos/x/barse/issues" && isIssueListRequest(r) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
			return
		}
		http.Error(w, "boom", http.StatusInternalServerError)
	})

	err := n.Notify(alertNotification())
	if err == nil {
		t.Fatal("expected error on non-201 response")
	}
}

func TestNotifySendsConfiguredLabelsAndMilestone(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/repos/x/barse/issues" && isIssueListRequest(r) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
			return
		}
		buf, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		gotBody = string(buf)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"number":9,"html_url":"https://git.naqerl.com/x/barse/issues/9"}`))
	}))
	defer srv.Close()

	n, err := New("issues", `{"base_url":"`+srv.URL+`","token":"sekret","owner":"x","repo":"barse","labels":[3,5],"milestone":7}`, NewCache(time.Minute))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := n.Notify(notify.Notification{
		Type:          notify.TypeAlert,
		CollectorName: "c",
		AppName:       "a",
		Payload:       notify.LogEntry{Message: "boom", GroupID: 1},
	}); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	if !strings.Contains(gotBody, `"labels":[3,5]`) {
		t.Errorf("expected labels [3,5] in body, got %q", gotBody)
	}
	if !strings.Contains(gotBody, `"milestone":7`) {
		t.Errorf("expected milestone 7 in body, got %q", gotBody)
	}
}

func TestNotifyMissingLogEntry(t *testing.T) {
	n, err := New("issues", `{"base_url":"https://example.invalid","token":"sekret","owner":"x","repo":"barse"}`, NewCache(time.Minute))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	err = n.Notify(notify.Notification{
		Type:          notify.TypeAlert,
		CollectorName: "c",
		AppName:       "a",
	})
	if err == nil {
		t.Fatal("expected error for missing log entry")
	}
}

func TestNotifyRejectsEmptyMessage(t *testing.T) {
	n := newTestNotifier(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("no network call expected for an empty message, got %s", r.URL.Path)
	})

	for _, msg := range []string{"", "  \n\t "} {
		err := n.Notify(notify.Notification{
			Type:          notify.TypeAlert,
			CollectorName: "c",
			AppName:       "a",
			Payload: notify.LogEntry{
				Message: msg,
				GroupID: 1,
			},
		})
		if err == nil {
			t.Errorf("expected error for message %q", msg)
		}
	}
}

func TestNewValidation(t *testing.T) {
	cases := []struct {
		name   string
		config string
	}{
		{"empty base url", `{"token":"t","owner":"o","repo":"r"}`},
		{"empty token", `{"base_url":"https://x","owner":"o","repo":"r"}`},
		{"empty owner", `{"base_url":"https://x","token":"t","repo":"r"}`},
		{"slash in owner", `{"base_url":"https://x","token":"t","owner":"o/p","repo":"r"}`},
		{"empty repo", `{"base_url":"https://x","token":"t","owner":"o"}`},
		{"slash in repo", `{"base_url":"https://x","token":"t","owner":"o","repo":"r/p"}`},
		{"space in owner", `{"base_url":"https://x","token":"t","owner":"o o","repo":"r"}`},
		{"query char in repo", `{"base_url":"https://x","token":"t","owner":"o","repo":"r?x"}`},
		{"invalid json", `not json`},
	}
	for _, c := range cases {
		if _, err := New("n", c.config, NewCache(time.Minute)); err == nil {
			t.Errorf("%s: expected error", c.name)
		}
	}
}

func TestMessageHashStable(t *testing.T) {
	a := messageHash("connection refused")
	b := messageHash("connection refused")
	if a != b {
		t.Fatalf("hash not stable: %q != %q", a, b)
	}
	if a == messageHash("connection reset") {
		t.Fatalf("hash collides across messages: %q", a)
	}
	if len(a) != 10 {
		t.Fatalf("expected 10-char base62 hash, got %q", a)
	}
}

func TestIssueTemplatesFormat(t *testing.T) {
	data := issueData{
		Hash:      "hash123456",
		App:       "api",
		Collector: "prod",
		GroupID:   7,
		FirstLine: "connection refused",
		Message:   "connection refused\nsecond line",
		LogURL:    "https://panel.example.com/logs/7",
	}

	var titleBuf bytes.Buffer
	if err := issueTemplates.ExecuteTemplate(&titleBuf, "title", data); err != nil {
		t.Fatalf("title: %v", err)
	}
	wantTitle := "connection refused"
	if titleBuf.String() != wantTitle {
		t.Errorf("title:\n got %q\nwant %q", titleBuf.String(), wantTitle)
	}

	var bodyBuf bytes.Buffer
	if err := issueTemplates.ExecuteTemplate(&bodyBuf, "body", data); err != nil {
		t.Fatalf("body: %v", err)
	}
	wantBody := "**App:** api\n**Collector:** prod\n**Group ID:** 7\n**Message hash:** `hash123456`\n\n```\nconnection refused\nsecond line\n```\n\n[view log](https://panel.example.com/logs/7)"
	if bodyBuf.String() != wantBody {
		t.Errorf("body:\n got %q\nwant %q", bodyBuf.String(), wantBody)
	}

	// No URL: the [view log] block is omitted and the body ends at the fence.
	data.LogURL = ""
	bodyBuf.Reset()
	if err := issueTemplates.ExecuteTemplate(&bodyBuf, "body", data); err != nil {
		t.Fatalf("body without url: %v", err)
	}
	wantNoURL := "**App:** api\n**Collector:** prod\n**Group ID:** 7\n**Message hash:** `hash123456`\n\n```\nconnection refused\nsecond line\n```"
	if bodyBuf.String() != wantNoURL {
		t.Errorf("body without url:\n got %q\nwant %q", bodyBuf.String(), wantNoURL)
	}
}
