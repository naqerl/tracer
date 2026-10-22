// Package mcp serves the panel to Model Context Protocol clients.
package mcp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"tracer/apps/panel"
	"tracer/apps/panel/internal/domain"
	authsvc "tracer/apps/panel/internal/services/auth"
	notifysvc "tracer/apps/panel/internal/services/notify"
	panelsvc "tracer/apps/panel/internal/services/panel"
	usersvc "tracer/apps/panel/internal/services/user"
	"tracer/apps/panel/web/middleware"
	"tracer/apps/panel/web/templates"
)

// maxContentBytes bounds one tool result so a single large page cannot flood a
// client's context window.
const maxContentBytes = 48 << 10

// sessionTimeout closes an MCP session that has gone quiet.
const sessionTimeout = 30 * time.Minute

// Server serves the panel to MCP clients.
type Server struct {
	deps     Deps
	server   *mcpsdk.Server
	mu       sync.Mutex
	demoUser *domain.User
}

// Deps is what the MCP endpoint needs from the panel.
type Deps struct {
	// Panel renders a panel route in-process, already wrapped so that
	// Accept: text/markdown comes back as markdown.
	Panel http.Handler
	// Service backs the write tools.
	Service *panelsvc.Service
	// Notify backs notifier creation.
	Notify *notifysvc.Service
	// Auth validates a caller's API key.
	Auth *authsvc.Service
	// User loads the API key's user.
	User *usersvc.Service
	// NoAuth reports the panel's -no-auth demo mode, where reads are open and
	// writes are refused.
	NoAuth bool
}

// New returns the panel's MCP server with every tool registered.
func New(deps Deps) *Server {
	s := &Server{deps: deps}
	s.server = mcpsdk.NewServer(&mcpsdk.Implementation{Name: "tracer", Version: panel.Version}, nil)
	s.registerTools()
	return s
}

// Handler returns the HTTP handler for the MCP endpoint.
func (s *Server) Handler() http.Handler {
	streamable := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server {
		return s.server
	}, &mcpsdk.StreamableHTTPOptions{
		SessionTimeout: sessionTimeout,
		// The panel is a networked server that sits behind a reverse proxy,
		// and that proxy reaches it over loopback, which the SDK's
		// DNS-rebinding guard rejects. Callers are authenticated by API key
		// instead. (In -no-auth demo mode there is no key; that panel is public
		// by design, so the guard would protect nothing there either.)
		DisableLocalhostProtection: true,
	})
	return s.requireAPIKey(streamable)
}

// requireAPIKey admits a request only when it carries a valid panel user API
// key. A session cookie is deliberately not accepted: MCP is token-only. In
// -no-auth mode the panel has no tokens to check, so the request passes and
// writes stay refused further down.
func (s *Server) requireAPIKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.deps.NoAuth {
			next.ServeHTTP(w, r)
			return
		}
		key := r.Header.Get("X-API-Key")
		if key == "" {
			http.Error(w, "X-API-Key header is required", http.StatusUnauthorized)
			return
		}
		if _, err := s.deps.Auth.ValidateUserAPIKey(r.Context(), key); err != nil {
			slog.Warn("MCP request rejected", "reason", "invalid API key", "error", err)
			http.Error(w, "invalid API key", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// caller resolves the user a tool call runs as: the key's user, or the demo
// admin when the panel runs without auth.
func (s *Server) caller(ctx context.Context, req *mcpsdk.CallToolRequest) (domain.User, error) {
	if s.deps.NoAuth {
		return s.demoAdmin(ctx)
	}
	var key string
	if req != nil && req.Extra != nil {
		key = req.Extra.Header.Get("X-API-Key")
	}
	if key == "" {
		return domain.User{}, errors.New("the request carried no X-API-Key header")
	}
	userID, err := s.deps.Auth.ValidateUserAPIKey(ctx, key)
	if err != nil {
		return domain.User{}, errors.New("the API key is invalid or revoked")
	}
	user, err := s.deps.User.FindByID(ctx, domain.System{}, userID)
	if err != nil {
		return domain.User{}, fmt.Errorf("could not load the API key's user: %w", err)
	}
	return user, nil
}

// demoAdmin resolves the user a -no-auth panel acts as, once.
func (s *Server) demoAdmin(ctx context.Context) (domain.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.demoUser != nil {
		return *s.demoUser, nil
	}
	users, err := s.deps.User.List(ctx, domain.System{})
	if err != nil {
		return domain.User{}, fmt.Errorf("could not list users: %w", err)
	}
	if len(users) == 0 {
		return domain.User{}, errors.New("the panel has no users yet")
	}
	picked := users[0]
	for _, user := range users {
		if user.Role == domain.RoleAdmin {
			picked = user
			break
		}
	}
	s.demoUser = &picked
	return picked, nil
}

// page renders one panel route in-process as the given user and returns its
// markdown. Reading through the panel's own mux keeps every permission check
// and the markdown rendering identical to a browser or API request. since is
// the panel's time window, passed the way the panel reads it.
func (s *Server) page(ctx context.Context, user domain.User, route string, query url.Values, since string) (string, error) {
	target := route
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", fmt.Errorf("could not build the panel request: %w", err)
	}
	req.Header.Set("Accept", "text/markdown")
	if since != "" {
		req.AddCookie(&http.Cookie{Name: templates.SinceCookieName, Value: since})
	}
	req = middleware.SetActor(req, user)

	rec := &recorder{header: make(http.Header)}
	s.deps.Panel.ServeHTTP(rec, req)
	status := rec.status
	if status == 0 {
		status = http.StatusOK
	}
	if status != http.StatusOK {
		return "", &panelError{status: status, route: route}
	}
	return rec.body.String(), nil
}

// readPage answers a tool call with one panel page as text content.
func (s *Server) readPage(ctx context.Context, req *mcpsdk.CallToolRequest, route string, query url.Values, since string) (*mcpsdk.CallToolResult, any, error) {
	user, err := s.caller(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	body, err := s.page(ctx, user, route, query, since)
	if err != nil {
		return nil, nil, toolError(err)
	}
	return textResult(truncate(body)), nil, nil
}

// recorder captures an in-process panel response.
type recorder struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (r *recorder) Header() http.Header {
	return r.header
}

func (r *recorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
}

func (r *recorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.body.Write(p)
}

// panelError is a non-200 response from the panel.
type panelError struct {
	status int
	route  string
}

func (e *panelError) Error() string {
	return fmt.Sprintf("panel answered HTTP %d for %s", e.status, e.route)
}

// statusText explains a non-200 panel response in terms a client can act on.
func statusText(e *panelError) string {
	switch e.status {
	case http.StatusBadRequest:
		return "the panel rejected the request as invalid (" + e.route + ")"
	case http.StatusForbidden:
		return "access denied: this API key's user has no access to that collector or app"
	case http.StatusNotFound:
		return "not found: the panel has no such collector, app, log group or log (" + e.route + ")"
	default:
		return fmt.Sprintf("the panel answered HTTP %d for %s", e.status, e.route)
	}
}

// toolError turns a panel failure into an error the client sees as a failed
// tool call instead of a protocol failure.
func toolError(err error) error {
	var panelErr *panelError
	if errors.As(err, &panelErr) {
		return errors.New(statusText(panelErr))
	}
	return err
}

// textResult wraps markdown as the single text content of a tool result.
func textResult(text string) *mcpsdk.CallToolResult {
	return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: text}}}
}

// truncate cuts text to maxContentBytes without splitting a rune and says what
// it dropped, so a large page still reaches the client.
func truncate(text string) string {
	if len(text) <= maxContentBytes {
		return text
	}
	cut := maxContentBytes
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	shown := strings.TrimRight(text[:cut], "\r\n")
	return shown + fmt.Sprintf("\n\n[truncated: showing the first %d of %d bytes read. Use a narrower since window, an offset, or a more specific tool.]", len(shown), len(text))
}
