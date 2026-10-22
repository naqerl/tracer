package mcp

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/crypto/bcrypt"

	"tracer/apps/panel/internal/config"
	"tracer/apps/panel/internal/domain"
	authsvc "tracer/apps/panel/internal/services/auth"
	ingestsvc "tracer/apps/panel/internal/services/ingest"
	notifysvc "tracer/apps/panel/internal/services/notify"
	forgejonotify "tracer/apps/panel/internal/services/notify/forgejo"
	ntfy "tracer/apps/panel/internal/services/notify/ntfy"
	telegram "tracer/apps/panel/internal/services/notify/telegram"
	panelsvc "tracer/apps/panel/internal/services/panel"
	usersvc "tracer/apps/panel/internal/services/user"
	"tracer/apps/panel/internal/testutil"
	"tracer/apps/panel/web/handlers/panel"
	"tracer/apps/panel/web/middleware"
	"tracer/libs/types"
)

func TestEndpointRequiresAPIKey(t *testing.T) {
	e := newEndpoint(t, false)
	key := e.userKey(t, e.admin.ID, "mcp")

	// No credential at all.
	if err := connectErr(t, e.url, nil); err == nil {
		t.Fatal("connecting without a key succeeded, want a 401")
	} else if !strings.Contains(strings.ToLower(err.Error()), "unauthorized") {
		t.Errorf("connect error = %v, want it to reject the request as unauthorized", err)
	}

	// A browser session cookie must not open the endpoint: MCP is token-only.
	token, err := e.auth.CreateSession(t.Context(), e.admin.ID)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	cookie := map[string]string{"Cookie": "tracer_session=" + token}
	if err := connectErr(t, e.url, cookie); err == nil {
		t.Fatal("connecting with only a session cookie succeeded, want a 401")
	}

	// A revoked key stays rejected.
	if err := e.auth.DeleteUserAPIKey(t.Context(), e.admin, e.userKeyID(t, e.admin.ID)); err != nil {
		t.Fatalf("delete user api key: %v", err)
	}
	if err := connectErr(t, e.url, map[string]string{"X-API-Key": key}); err == nil {
		t.Fatal("connecting with a revoked key succeeded, want a 401")
	}
}

func TestToolList(t *testing.T) {
	e := newEndpoint(t, false)
	session := e.connect(t, e.userKey(t, e.admin.ID, "mcp"))

	res, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	readOnly := map[string]bool{}
	for _, tool := range res.Tools {
		readOnly[tool.Name] = tool.Annotations != nil && tool.Annotations.ReadOnlyHint
	}
	if len(readOnly) != 13 {
		t.Fatalf("listed %d tools, want 12: %v", len(readOnly), readOnly)
	}
	for name, only := range readOnly {
		if name == "create_app" || name == "create_notifier" {
			if only {
				t.Errorf("%s is marked read-only", name)
			}
			continue
		}
		if !only {
			t.Errorf("tool %s is not marked read-only", name)
		}
	}
}

func TestReadToolsAnswerFromPanel(t *testing.T) {
	e := newEndpoint(t, false)
	session := e.connect(t, e.userKey(t, e.admin.ID, "mcp"))

	collectors := e.call(t, session, "list_collectors", nil)
	if !strings.Contains(collectors, "demo") {
		t.Errorf("list_collectors = %q, want it to name the demo collector", collectors)
	}

	groups := e.call(t, session, "list_log_groups", map[string]any{
		"collector": "demo", "app": "myapp", "level": "all", "since": "1h",
	})
	if !strings.Contains(groups, "connection refused") {
		t.Errorf("list_log_groups = %q, want the seeded message", groups)
	}

	group := e.call(t, session, "get_log_group", map[string]any{
		"collector": "demo", "app": "myapp", "group_id": e.groupID,
	})
	if !strings.Contains(group, "connection refused") {
		t.Errorf("get_log_group = %q, want the seeded message", group)
	}

	entry := e.call(t, session, "get_log", map[string]any{
		"collector": "demo", "app": "myapp", "id": e.logID,
	})
	if !strings.Contains(entry, "connection refused") {
		t.Errorf("get_log = %q, want the seeded message", entry)
	}
}

func TestToolRoutes(t *testing.T) {
	e := newEndpoint(t, false)
	session := e.connect(t, e.userKey(t, e.admin.ID, "mcp"))
	base := "/collectors/demo/apps/myapp"

	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{name: "list_collectors", want: "/"},
		{name: "get_collector", args: map[string]any{"collector": "demo"}, want: "/collectors/demo"},
		{name: "collector_resources", args: map[string]any{"collector": "demo"}, want: "/collectors/demo/resources"},
		{name: "list_apps", args: map[string]any{"collector": "demo"}, want: "/collectors/demo/apps"},
		{name: "get_app", args: map[string]any{"collector": "demo", "app": "myapp"}, want: base + "?level=error"},
		{name: "app_stats", args: map[string]any{"collector": "demo", "app": "myapp"}, want: base + "/stats"},
		{name: "app_uptime", args: map[string]any{"collector": "demo", "app": "myapp"}, want: base + "/uptime"},
		{name: "list_log_groups", args: map[string]any{"collector": "demo", "app": "myapp"}, want: base + "/logs/groups?level=error"},
		{name: "get_log_group", args: map[string]any{"collector": "demo", "app": "myapp", "group_id": e.groupID}, want: base + "/logs/groups/" + strconv.FormatInt(e.groupID, 10)},
		{name: "log_group_entries", args: map[string]any{"collector": "demo", "app": "myapp", "group_id": e.groupID}, want: base + "/logs/groups/" + strconv.FormatInt(e.groupID, 10) + "/entries"},
		{name: "get_log", args: map[string]any{"collector": "demo", "app": "myapp", "id": e.logID}, want: base + "/logs/" + strconv.FormatInt(e.logID, 10)},
	}
	if len(cases) != 11 {
		t.Fatalf("route table covers %d tools, want 11", len(cases))
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e.panel.forget()
			res, err := session.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: tc.name, Arguments: tc.args})
			if err != nil {
				t.Fatalf("call %s: %v", tc.name, err)
			}
			if res.IsError {
				t.Fatalf("%s reported an error: %s", tc.name, contentText(t, res))
			}
			if got := e.panel.last(); got != tc.want {
				t.Errorf("%s read %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

func TestCreateAppWritesWithPermission(t *testing.T) {
	e := newEndpoint(t, false)
	session := e.connect(t, e.userKey(t, e.admin.ID, "mcp"))

	res, err := session.CallTool(t.Context(), &mcpsdk.CallToolParams{
		Name: "create_app",
		Arguments: map[string]any{
			"collector": "demo",
			"name":      "checkout",
			"url":       "http://127.0.0.1:1/healthz",
		},
	})
	if err != nil {
		t.Fatalf("call create_app: %v", err)
	}
	if res.IsError {
		t.Fatalf("create_app reported an error: %s", contentText(t, res))
	}

	apps, err := e.panelSvc.ListCollectorApps(t.Context(), panelsvc.ListCollectorAppsRequest{UserID: e.admin.ID, CollectorName: "demo"})
	if err != nil {
		t.Fatalf("list apps: %v", err)
	}
	found := false
	for _, name := range apps {
		if name == "checkout" {
			found = true
		}
	}
	if !found {
		t.Errorf("apps = %v, want the created app", apps)
	}
}

func TestCreateAppNeedsPermission(t *testing.T) {
	e := newEndpoint(t, false)
	stranger, err := e.auth.Register(t.Context(), authsvc.RegisterRequest{Username: "bob", Password: "secret"})
	if err != nil {
		t.Fatalf("register stranger: %v", err)
	}
	session := e.connect(t, e.userKey(t, stranger.ID, "bob"))

	// Reading a collector the user has no access to is denied by the panel.
	denied := callTool(t, session, "get_collector", map[string]any{"collector": "demo"})
	if !denied.IsError {
		t.Error("get_collector for an unassigned collector succeeded, want access denied")
	} else if text := contentText(t, denied); !strings.Contains(text, "access denied") {
		t.Errorf("get_collector error = %q, want access denied", text)
	}

	// The write tool must refuse rather than rely on the service, which does
	// no permission check of its own.
	res := callTool(t, session, "create_app", map[string]any{
		"collector": "demo", "name": "sneaky", "url": "http://127.0.0.1:1/healthz",
	})
	if !res.IsError {
		t.Fatal("create_app succeeded without permission")
	}
	if text := contentText(t, res); !strings.Contains(text, "not allowed") {
		t.Errorf("create_app error = %q, want a permission refusal", text)
	}
}

func TestNoAuthDemoReadsButCannotCreate(t *testing.T) {
	e := newEndpoint(t, true)
	// No API key: the demo has no keys to check.
	session := connect(t, e.url, nil)

	collectors := e.call(t, session, "list_collectors", nil)
	if !strings.Contains(collectors, "demo") {
		t.Errorf("list_collectors = %q, want it to work without a key in demo mode", collectors)
	}

	res := callTool(t, session, "create_app", map[string]any{
		"collector": "demo", "name": "sneaky", "url": "http://127.0.0.1:1/healthz",
	})
	if !res.IsError {
		t.Fatal("create_app succeeded on a no-auth demo panel")
	}
	if text := contentText(t, res); !strings.Contains(text, "demo mode") {
		t.Errorf("create_app error = %q, want it to name demo mode", text)
	}
}

func TestInvalidInput(t *testing.T) {
	e := newEndpoint(t, false)
	session := e.connect(t, e.userKey(t, e.admin.ID, "mcp"))

	cases := []struct {
		name string
		tool string
		args map[string]any
		want string
	}{
		{name: "bad since", tool: "list_apps", args: map[string]any{"collector": "demo", "since": "2h"}, want: "invalid since"},
		{name: "bad level", tool: "list_log_groups", args: map[string]any{"collector": "demo", "app": "myapp", "level": "verbose"}, want: "invalid level"},
		{name: "negative offset", tool: "log_group_entries", args: map[string]any{"collector": "demo", "app": "myapp", "group_id": 1, "offset": -1}, want: "offset must not be negative"},
		{name: "bad group id", tool: "get_log_group", args: map[string]any{"collector": "demo", "app": "myapp", "group_id": 0}, want: "group_id must be a positive number"},
		{name: "missing field", tool: "list_apps", args: map[string]any{}, want: "collector"},
		{name: "absent app", tool: "get_app", args: map[string]any{"collector": "demo", "app": "nope"}, want: "not found"},
		{name: "parent-directory collector", tool: "create_app", args: map[string]any{
			"collector": "..", "name": "sneaky", "url": "http://127.0.0.1:1/healthz",
		}, want: "invalid collector name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := callTool(t, session, tc.tool, tc.args)
			if !res.IsError {
				t.Fatalf("%s(%v) succeeded, want a tool error", tc.tool, tc.args)
			}
			if text := contentText(t, res); !strings.Contains(text, tc.want) {
				t.Errorf("error = %q, want it to contain %q", text, tc.want)
			}
		})
	}
}

func TestToolEscapesNamesIntoTheRoute(t *testing.T) {
	// A name with a slash must stay one path segment, so it cannot redirect the
	// tool at a different panel route.
	e := newEndpoint(t, false)
	session := e.connect(t, e.userKey(t, e.admin.ID, "mcp"))

	e.panel.forget()
	// The collector does not exist, so the call is expected to fail; what
	// matters is the route the panel was asked for.
	_ = callTool(t, session, "get_collector", map[string]any{"collector": "demo/../../settings"})

	got := e.panel.last()
	if want := "/collectors/demo%2F..%2F..%2Fsettings"; got != want {
		t.Errorf("panel was asked for %q, want %q", got, want)
	}
}

func TestToolCallsAreLogged(t *testing.T) {
	// The panel's log is how an operator sees what an agent asked for, so the
	// tool name and its arguments must reach it.
	e := newEndpoint(t, false)
	session := e.connect(t, e.userKey(t, e.admin.ID, "mcp"))

	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	e.call(t, session, "list_log_groups", map[string]any{
		"collector": "demo", "app": "myapp", "level": "all",
	})
	logged := buf.String()
	if !strings.Contains(logged, "MCP tool call") {
		t.Fatalf("the log does not record the tool call: %q", logged)
	}
	if !strings.Contains(logged, "tool=list_log_groups") {
		t.Errorf("the log does not name the tool: %q", logged)
	}
	// slog's text handler quotes a string value with strconv.Quote.
	wantArgs := "args=" + strconv.Quote(`{"collector":"demo","app":"myapp","level":"all"}`)
	if !strings.Contains(logged, wantArgs) {
		t.Errorf("the log does not carry the arguments (want %s): %q", wantArgs, logged)
	}

	// A refused call is logged too, marked as an error rather than hidden.
	buf.Reset()
	_ = callTool(t, session, "get_app", map[string]any{"collector": "demo", "app": "nope"})
	if failed := buf.String(); !strings.Contains(failed, "isError=true") {
		t.Errorf("a failed call is not marked in the log: %q", failed)
	}
}

func TestTruncateKeepsValidUTF8(t *testing.T) {
	// The odd leading byte puts the limit in the middle of a two-byte rune.
	body := "a" + strings.Repeat("é", maxContentBytes)
	got := truncate(body)
	if !utf8.ValidString(got) {
		t.Error("truncated text is not valid UTF-8")
	}
	if !strings.Contains(got, "[truncated: showing") {
		t.Error("truncated text is missing the note")
	}
	if len(got) > maxContentBytes+200 {
		t.Errorf("truncated text is %d bytes, want it capped near %d", len(got), maxContentBytes)
	}
}

// ── Fixture ─────────────────────────────────────────────────────────────────

// endpoint is a real panel mux behind the MCP endpoint, with one collector and
// app and one stored error log.
type endpoint struct {
	url      string
	panelSvc *panelsvc.Service
	auth     *authsvc.Service
	admin    domain.User
	groupID  int64
	logID    int64
	panel    *spyPanel
}

// newEndpoint builds the panel services, the panel's own mux, and the MCP
// endpoint over it. noAuth mirrors the panel's -no-auth demo mode.
func newEndpoint(t *testing.T, noAuth bool) *endpoint {
	t.Helper()
	handle := testutil.NewTestDB(t)
	auth := authsvc.New(handle, testutil.TestKeySecret(), bcrypt.MinCost)
	users := usersvc.NewService(handle)

	admin, err := auth.Register(t.Context(), authsvc.RegisterRequest{Username: "admin", Password: "secret", IsAdmin: true})
	if err != nil {
		t.Fatalf("register admin: %v", err)
	}

	dbMgr := testutil.NewTestDBManager(t)
	if _, err := dbMgr.CreateCollectorDB("demo"); err != nil {
		t.Fatalf("create collector db: %v", err)
	}
	if _, err := dbMgr.CreateAppDB("demo", "myapp"); err != nil {
		t.Fatalf("create app db: %v", err)
	}

	dataDir := t.TempDir()
	cfg := config.New(config.Panel{
		Port:          "8080",
		DataDir:       dataDir,
		Host:          "https://tracer.example.com",
		SampleRate:    0.1,
		RetentionDays: 90,
	}, filepath.Join(dataDir, "config.yaml"))
	ingest := ingestsvc.New(dbMgr, make(chan notifysvc.Notification, 10), func(_, _ string, _ int64) string { return "" }, cfg, nil)
	panelSvc := panelsvc.New(dbMgr, ingest, ingestsvc.NewTracker(), users, nil, nil)

	forgejoCache := forgejonotify.NewCache(forgejonotify.IssueCacheTTL)
	notifierFactories := map[string]notifysvc.FromRawJSON{
		"telegram": telegram.FromRawJSON,
		"ntfy":     ntfy.FromRawJSON,
		"forgejo": func(name, config string) (notifysvc.Notifier, error) {
			return forgejonotify.New(name, config, forgejoCache)
		},
	}
	notifySvc := notifysvc.New(handle, users, dbMgr, notifierFactories)

	if _, err := ingest.StoreLog(t.Context(), ingestsvc.StoreLogRequest{
		CollectorName: "demo",
		AppName:       "myapp",
		Entry:         types.LogEntry{Level: types.LevelError, Message: "connection refused", Timestamp: time.Now()},
	}); err != nil {
		t.Fatalf("seed log: %v", err)
	}
	e := &endpoint{panelSvc: panelSvc, auth: auth, admin: admin}
	e.groupID, e.logID = seedIDs(t, panelSvc, admin.ID)

	panels := panel.InitMux(panelSvc, auth)
	e.panel = &spyPanel{handler: middleware.BuildMarkdownAcceptMiddleware()(panels)}
	srv := New(Deps{Panel: e.panel, Service: panelSvc, Notify: notifySvc, Auth: auth, User: users, NoAuth: noAuth})
	httpSrv := httptest.NewServer(srv.Handler())
	t.Cleanup(httpSrv.Close)
	e.url = httpSrv.URL
	return e
}

// userKey mints a user API key and returns its plaintext.
func (e *endpoint) userKey(t *testing.T, userID int64, name string) string {
	t.Helper()
	created, err := e.auth.GenerateUserAPIKey(t.Context(), domain.System{}, authsvc.GenerateUserAPIKeyRequest{UserID: userID, Name: name})
	if err != nil {
		t.Fatalf("generate user api key: %v", err)
	}
	return created.Plaintext
}

// userKeyID resolves the id of the user's newest API key.
func (e *endpoint) userKeyID(t *testing.T, userID int64) int64 {
	t.Helper()
	keys, err := e.auth.ListUserAPIKeys(t.Context(), domain.System{}, userID)
	if err != nil {
		t.Fatalf("list user api keys: %v", err)
	}
	if len(keys) == 0 {
		t.Fatal("the user has no API keys")
	}
	return keys[0].ID
}

func (e *endpoint) connect(t *testing.T, key string) *mcpsdk.ClientSession {
	t.Helper()
	return connect(t, e.url, map[string]string{"X-API-Key": key})
}

// call runs a tool that takes no arguments and returns its text.
func (e *endpoint) call(t *testing.T, session *mcpsdk.ClientSession, name string, args map[string]any) string {
	t.Helper()
	res := callTool(t, session, name, args)
	if res.IsError {
		t.Fatalf("%s reported an error: %s", name, contentText(t, res))
	}
	return contentText(t, res)
}

// seedIDs resolves the seeded log group and log entry ids through the public
// panel API.
func seedIDs(t *testing.T, panelSvc *panelsvc.Service, userID int64) (int64, int64) {
	t.Helper()
	groups, err := panelSvc.GetLogGroups(t.Context(), panelsvc.GetLogGroupsRequest{
		UserID:        userID,
		CollectorName: "demo",
		AppName:       "myapp",
		Level:         "error",
		Since:         time.Now().Add(-time.Hour),
		Limit:         50,
	})
	if err != nil {
		t.Fatalf("list log groups: %v", err)
	}
	if len(groups) == 0 {
		t.Fatal("the seeded log group was not found")
	}
	entries, err := panelSvc.ListGroupLogs(t.Context(), panelsvc.ListGroupLogsRequest{
		UserID:        userID,
		CollectorName: "demo",
		AppName:       "myapp",
		GroupID:       groups[0].ID,
		Since:         time.Now().Add(-time.Hour),
		Limit:         10,
	})
	if err != nil {
		t.Fatalf("list group logs: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("the seeded log entry was not found")
	}
	return groups[0].ID, entries[0].ID
}

// ── Helpers ─────────────────────────────────────────────────────────────────

// spyPanel records the last panel route an MCP tool read, then renders it.
type spyPanel struct {
	handler http.Handler
	mu      sync.Mutex
	lastURL string
}

func (s *spyPanel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// EscapedPath, not Path: a collector or app name must not be able to add
	// path segments to the route the tool reads.
	route := r.URL.EscapedPath()
	if r.URL.RawQuery != "" {
		route += "?" + r.URL.RawQuery
	}
	s.mu.Lock()
	s.lastURL = route
	s.mu.Unlock()
	s.handler.ServeHTTP(w, r)
}

func (s *spyPanel) forget() {
	s.mu.Lock()
	s.lastURL = ""
	s.mu.Unlock()
}

func (s *spyPanel) last() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastURL
}

// headerTransport adds headers to every MCP request.
type headerTransport struct {
	headers map[string]string
}

func (h headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if len(h.headers) > 0 {
		req = req.Clone(req.Context())
		for name, value := range h.headers {
			req.Header.Set(name, value)
		}
	}
	return http.DefaultTransport.RoundTrip(req)
}

func connect(t *testing.T, endpoint string, headers map[string]string) *mcpsdk.ClientSession {
	t.Helper()
	session, err := newClient().Connect(t.Context(), transport(endpoint, headers), nil)
	if err != nil {
		t.Fatalf("could not connect to %s: %v", endpoint, err)
	}
	t.Cleanup(func() {
		if err := session.Close(); err != nil {
			t.Errorf("could not close the session: %v", err)
		}
	})
	return session
}

// connectErr returns the connection error instead of failing the test, for the
// requests that are meant to be rejected.
func connectErr(t *testing.T, endpoint string, headers map[string]string) error {
	t.Helper()
	session, err := newClient().Connect(t.Context(), transport(endpoint, headers), nil)
	if err != nil {
		return err
	}
	if err := session.Close(); err != nil {
		t.Errorf("could not close the session: %v", err)
	}
	return nil
}

func newClient() *mcpsdk.Client {
	return mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
}

func transport(endpoint string, headers map[string]string) *mcpsdk.StreamableClientTransport {
	return &mcpsdk.StreamableClientTransport{
		Endpoint:   endpoint,
		HTTPClient: &http.Client{Transport: headerTransport{headers: headers}},
		// Responses only; the standalone SSE stream is not needed here.
		DisableStandaloneSSE: true,
	}
}

func callTool(t *testing.T, session *mcpsdk.ClientSession, name string, args map[string]any) *mcpsdk.CallToolResult {
	t.Helper()
	res, err := session.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s returned a protocol error: %v", name, err)
	}
	return res
}

func contentText(t *testing.T, res *mcpsdk.CallToolResult) string {
	t.Helper()
	if len(res.Content) != 1 {
		t.Fatalf("result has %d content items, want 1", len(res.Content))
	}
	text, ok := res.Content[0].(*mcpsdk.TextContent)
	if !ok {
		t.Fatalf("content is %T, want *mcpsdk.TextContent", res.Content[0])
	}
	return text.Text
}
