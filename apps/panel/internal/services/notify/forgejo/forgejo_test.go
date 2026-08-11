package forgejo

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"tracer/apps/panel/internal/services/notify"
)

type fakeRecorder struct {
	calls []recordCall
}

type recordCall struct {
	collectorName string
	appName       string
	groupID       int64
	issueNumber   int64
	issueURL      string
}

func (r *fakeRecorder) RecordGroupIssue(_ context.Context, collectorName, appName string, groupID, issueNumber int64, issueURL string) error {
	r.calls = append(r.calls, recordCall{collectorName, appName, groupID, issueNumber, issueURL})
	return nil
}

func TestNotifyCreatesIssue(t *testing.T) {
	var gotPath, gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		buf, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		gotBody = string(buf)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"number": 42, "html_url": "https://git.naqerl.com/x/barse/issues/42"}`))
	}))
	defer srv.Close()

	rec := &fakeRecorder{}
	n, err := New("issues", `{"base_url":"`+srv.URL+`","token":"sekret","owner":"x","repo":"barse"}`, rec)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	err = n.Notify(notify.Notification{
		Type:          notify.TypeNewErrorGroup,
		CollectorName: "prod",
		AppName:       "api",
		Payload: notify.LogEntry{
			Message: "connection refused\nsecond line",
			GroupID: 7,
			LogURL:  "https://panel.example.com/panel/collectors/prod/apps/api/logs/groups/7",
		},
	})
	if err != nil {
		t.Fatalf("Notify: %v", err)
	}

	if gotPath != "/api/v1/repos/x/barse/issues" {
		t.Errorf("expected issue path, got %q", gotPath)
	}
	if gotAuth != "token sekret" {
		t.Errorf("expected token auth header, got %q", gotAuth)
	}

	var body struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if err := json.Unmarshal([]byte(gotBody), &body); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	if !strings.HasPrefix(body.Title, "[tracer] api@prod: connection refused") {
		t.Errorf("unexpected title: %q", body.Title)
	}
	if !strings.Contains(body.Body, "https://panel.example.com/panel/collectors/prod/apps/api/logs/groups/7") {
		t.Errorf("expected group link in body, got: %q", body.Body)
	}

	if len(rec.calls) != 1 {
		t.Fatalf("expected 1 recorded issue, got %d", len(rec.calls))
	}
	call := rec.calls[0]
	if call.collectorName != "prod" || call.appName != "api" || call.groupID != 7 || call.issueNumber != 42 || call.issueURL != "https://git.naqerl.com/x/barse/issues/42" {
		t.Errorf("unexpected recorded call: %+v", call)
	}
}

func TestNotifyTruncatesLongTitle(t *testing.T) {
	var gotTitle string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Title string `json:"title"`
		}
		buf, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		_ = json.Unmarshal(buf, &body)
		gotTitle = body.Title
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"number": 1, "html_url": "https://git.naqerl.com/x/barse/issues/1"}`))
	}))
	defer srv.Close()

	rec := &fakeRecorder{}
	n, err := New("issues", `{"base_url":"`+srv.URL+`","token":"sekret","owner":"x","repo":"barse"}`, rec)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// 200 multi-byte runes exceed Forgejo's 255-char title limit when counted
	// in bytes; the title must be cut to 255 bytes without splitting a rune.
	long := strings.Repeat("界", 200)
	if err := n.Notify(notify.Notification{
		Type:          notify.TypeNewErrorGroup,
		CollectorName: "c",
		AppName:       "a",
		Payload: notify.LogEntry{
			Message: long,
			GroupID: 1,
		},
	}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if len(rec.calls) != 1 || rec.calls[0].issueNumber != 1 {
		t.Fatalf("expected issue recorded, got %+v", rec.calls)
	}
	if len(gotTitle) > 255 {
		t.Errorf("title exceeds 255 bytes, got %d: %q", len(gotTitle), gotTitle)
	}
	if !utf8.ValidString(gotTitle) {
		t.Errorf("title splits a UTF-8 sequence: %q", gotTitle)
	}
}

func TestNotifyIgnoresOtherTypes(t *testing.T) {
	rec := &fakeRecorder{}
	n, err := New("issues", `{"base_url":"https://example.invalid","token":"sekret","owner":"x","repo":"barse"}`, rec)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	for _, typ := range []notify.NotificationType{notify.TypeAlert, notify.TypeLifecycle, notify.TypeSubscription} {
		if err := n.Notify(notify.Notification{Type: typ}); err != nil {
			t.Fatalf("Notify(%s): %v", typ, err)
		}
	}
	if len(rec.calls) != 0 {
		t.Fatalf("expected no recorded issues, got %+v", rec.calls)
	}
}

func TestWelcomeValidatesConnection(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"full_name":"x/barse"}`))
	}))
	defer srv.Close()

	rec := &fakeRecorder{}
	n, err := New("issues", `{"base_url":"`+srv.URL+`","token":"sekret","owner":"x","repo":"barse"}`, rec)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := n.Notify(notify.Notification{Type: notify.TypeWelcome}); err != nil {
		t.Fatalf("welcome: %v", err)
	}
	if gotPath != "/api/v1/repos/x/barse" {
		t.Errorf("expected repo validation path, got %q", gotPath)
	}
	if len(rec.calls) != 0 {
		t.Fatalf("welcome must not create issues, got %+v", rec.calls)
	}
}

func TestWelcomeRejectsUnreachableRepo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	n, err := New("issues", `{"base_url":"`+srv.URL+`","token":"badtoken","owner":"x","repo":"barse"}`, &fakeRecorder{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	err = n.Notify(notify.Notification{Type: notify.TypeWelcome})
	if err == nil {
		t.Fatal("expected error for unauthorized repo validation")
	}
	if !errors.Is(err, notify.ErrWelcomeValidation) {
		t.Fatalf("expected ErrWelcomeValidation, got %v", err)
	}
}

func TestNotifyNon2xxReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	rec := &fakeRecorder{}
	n, err := New("issues", `{"base_url":"`+srv.URL+`","token":"sekret","owner":"x","repo":"barse"}`, rec)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	err = n.Notify(notify.Notification{
		Type:          notify.TypeNewErrorGroup,
		CollectorName: "c",
		AppName:       "a",
		Payload: notify.LogEntry{
			Message: "boom",
			GroupID: 1,
		},
	})
	if err == nil {
		t.Fatal("expected error on non-201 response")
	}
	if len(rec.calls) != 0 {
		t.Fatalf("expected no recorded issues on failure, got %+v", rec.calls)
	}
}

func TestNotifySendsConfiguredLabelsAndMilestone(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		gotBody = string(buf)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"number":9,"html_url":"https://git.naqerl.com/x/barse/issues/9"}`))
	}))
	defer srv.Close()

	rec := &fakeRecorder{}
	n, err := New("issues", `{"base_url":"`+srv.URL+`","token":"sekret","owner":"x","repo":"barse","labels":[3,5],"milestone":7}`, rec)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := n.Notify(notify.Notification{
		Type:          notify.TypeNewErrorGroup,
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
	if len(rec.calls) != 1 {
		t.Fatalf("expected 1 recorded issue, got %d", len(rec.calls))
	}
}

func TestNotifyMissingLogEntry(t *testing.T) {
	n, err := New("issues", `{"base_url":"https://example.invalid","token":"sekret","owner":"x","repo":"barse"}`, &fakeRecorder{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	err = n.Notify(notify.Notification{
		Type:          notify.TypeNewErrorGroup,
		CollectorName: "c",
		AppName:       "a",
	})
	if err == nil {
		t.Fatal("expected error for missing log entry")
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
		if _, err := New("n", c.config, &fakeRecorder{}); err == nil {
			t.Errorf("%s: expected error", c.name)
		}
	}
}
