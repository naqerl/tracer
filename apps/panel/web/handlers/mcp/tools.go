package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"tracer/apps/panel/internal/domain"
	panelsvc "tracer/apps/panel/internal/services/panel"
	"tracer/apps/panel/web/templates"
)

// validLevel mirrors the panel's log-level filters; the panel would silently
// fall back to its own default otherwise.
var validLevel = map[string]bool{"all": true, "error": true, "warn": true, "info": true}

// ── Tool input schemas ──────────────────────────────────────────────────────

type collectorInput struct {
	Collector string `json:"collector" jsonschema:"collector name, as returned by list_collectors"`
}

type sinceInput struct {
	Collector string `json:"collector" jsonschema:"collector name, as returned by list_collectors"`
	Since     string `json:"since,omitempty" jsonschema:"time window: 1h, 6h, 24h, 7d or 30d (default 24h)"`
}

type appSinceInput struct {
	Collector string `json:"collector" jsonschema:"collector name, as returned by list_collectors"`
	App       string `json:"app" jsonschema:"app name, as returned by list_apps"`
	Since     string `json:"since,omitempty" jsonschema:"time window: 1h, 6h, 24h, 7d or 30d (default 24h)"`
}

type listLogGroupsInput struct {
	Collector string `json:"collector" jsonschema:"collector name, as returned by list_collectors"`
	App       string `json:"app" jsonschema:"app name, as returned by list_apps"`
	Level     string `json:"level,omitempty" jsonschema:"log level filter: all, error, warn or info (default error)"`
	Since     string `json:"since,omitempty" jsonschema:"time window: 1h, 6h, 24h, 7d or 30d (default 24h)"`
	Offset    int    `json:"offset,omitempty" jsonschema:"number of groups to skip, for paging (default 0)"`
}

type logGroupInput struct {
	Collector string `json:"collector" jsonschema:"collector name, as returned by list_collectors"`
	App       string `json:"app" jsonschema:"app name, as returned by list_apps"`
	GroupID   int64  `json:"group_id" jsonschema:"log group id, as returned by list_log_groups"`
	Since     string `json:"since,omitempty" jsonschema:"time window: 1h, 6h, 24h, 7d or 30d (default 24h)"`
}

type logGroupEntriesInput struct {
	Collector string `json:"collector" jsonschema:"collector name, as returned by list_collectors"`
	App       string `json:"app" jsonschema:"app name, as returned by list_apps"`
	GroupID   int64  `json:"group_id" jsonschema:"log group id, as returned by list_log_groups"`
	Since     string `json:"since,omitempty" jsonschema:"time window: 1h, 6h, 24h, 7d or 30d (default 24h)"`
	Offset    int    `json:"offset,omitempty" jsonschema:"number of entries to skip, for paging (default 0)"`
}

type logInput struct {
	Collector string `json:"collector" jsonschema:"collector name, as returned by list_collectors"`
	App       string `json:"app" jsonschema:"app name, as returned by list_apps"`
	ID        int64  `json:"id" jsonschema:"log entry id, as returned by log_group_entries"`
}

type createAppInput struct {
	Collector      string `json:"collector" jsonschema:"collector name, as returned by list_collectors"`
	Name           string `json:"name" jsonschema:"name for the new app; names are unique per collector"`
	Service        string `json:"service,omitempty" jsonschema:"log source: a systemd unit, a kubectl workload, or a file path. Empty creates an uptime-only app"`
	URL            string `json:"url,omitempty" jsonschema:"optional HTTP health-check URL to poll"`
	Interval       string `json:"interval,omitempty" jsonschema:"how often to run the health check, e.g. 30s (default 60s)"`
	ExpectedStatus string `json:"expected_status,omitempty" jsonschema:"comma-separated HTTP status codes that count as up, e.g. 200,301 (default any 2xx)"`
}

// ── Tool registration ───────────────────────────────────────────────────────

func (s *Server) registerTools() {
	mcpsdk.AddTool(s.server, readOnlyTool("list_collectors",
		"List every collector the caller can see, with connection status, system stats and app counts. Start here to find collector names."),
		s.listCollectors)
	mcpsdk.AddTool(s.server, readOnlyTool("get_collector",
		"Show one collector's overview: connection state, CPU/RAM/disk snapshot and how many apps it has."),
		s.getCollector)
	mcpsdk.AddTool(s.server, readOnlyTool("collector_resources",
		"CPU, RAM and disk history for one collector over a time window."),
		s.collectorResources)
	mcpsdk.AddTool(s.server, readOnlyTool("list_apps",
		"List the apps on one collector with uptime bars, error counts and the error trend for the window, so the failing apps stand out."),
		s.listApps)
	mcpsdk.AddTool(s.server, readOnlyTool("get_app",
		"Show one app's page: its uptime for the window, with links to its log stats and log groups. Call list_log_groups for the app's error groups and app_stats for the log counts."),
		s.getApp)
	mcpsdk.AddTool(s.server, readOnlyTool("app_stats",
		"Log volume for one app over the window: totals and the error, warning and info split."),
		s.appStats)
	mcpsdk.AddTool(s.server, readOnlyTool("app_uptime",
		"Uptime percentage and per-bucket bars for one app over the window."),
		s.appUptime)
	mcpsdk.AddTool(s.server, readOnlyTool("list_log_groups",
		"Grouped, deduplicated log messages for one app, newest first, with an occurrence count per group. This is where failures show up; pass level=all to include info and warnings."),
		s.listLogGroups)
	mcpsdk.AddTool(s.server, readOnlyTool("get_log_group",
		"One log group in full: its message, level and count. Use log_group_entries for the individual entries."),
		s.getLogGroup)
	mcpsdk.AddTool(s.server, readOnlyTool("log_group_entries",
		"Individual log entries inside one group, each with its timestamp and context. Use offset to page; the panel returns 50 per page."),
		s.logGroupEntries)
	mcpsdk.AddTool(s.server, readOnlyTool("get_log",
		"One log entry in full: message, level, time, trace id and structured context."),
		s.getLog)
	mcpsdk.AddTool(s.server, createAppTool(),
		s.createApp)
}

// readOnlyTool returns a tool marked as read-only, which is every tracer tool
// that only reads panel pages.
func readOnlyTool(name, description string) *mcpsdk.Tool {
	return &mcpsdk.Tool{
		Name:        name,
		Description: description,
		Annotations: &mcpsdk.ToolAnnotations{ReadOnlyHint: true},
	}
}

// createAppTool returns the app-creation tool. It is additive and not
// idempotent: a second call with the same name fails on the taken name.
func createAppTool() *mcpsdk.Tool {
	destructive := false
	return &mcpsdk.Tool{
		Name: "create_app",
		Description: "Add an app to a collector. Give a service (systemd unit, kubectl workload or file path) to collect its logs, " +
			"or a URL to watch it over HTTP, or both. The collector resolves the log format when a service is given. " +
			"Not available on a panel that runs without authentication (demo mode), and requires permission to modify the collector.",
		Annotations: &mcpsdk.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &destructive},
	}
}

// ── Read tool handlers ──────────────────────────────────────────────────────

func (s *Server) listCollectors(ctx context.Context, req *mcpsdk.CallToolRequest, _ struct{}) (*mcpsdk.CallToolResult, any, error) {
	return s.readPage(ctx, req, "/", nil, "")
}

func (s *Server) getCollector(ctx context.Context, req *mcpsdk.CallToolRequest, in collectorInput) (*mcpsdk.CallToolResult, any, error) {
	return s.readPage(ctx, req, "/collectors/"+url.PathEscape(in.Collector), nil, "")
}

func (s *Server) collectorResources(ctx context.Context, req *mcpsdk.CallToolRequest, in sinceInput) (*mcpsdk.CallToolResult, any, error) {
	if err := validateSince(in.Since); err != nil {
		return nil, nil, err
	}
	return s.readPage(ctx, req, "/collectors/"+url.PathEscape(in.Collector)+"/resources", nil, in.Since)
}

func (s *Server) listApps(ctx context.Context, req *mcpsdk.CallToolRequest, in sinceInput) (*mcpsdk.CallToolResult, any, error) {
	if err := validateSince(in.Since); err != nil {
		return nil, nil, err
	}
	return s.readPage(ctx, req, "/collectors/"+url.PathEscape(in.Collector)+"/apps", nil, in.Since)
}

func (s *Server) getApp(ctx context.Context, req *mcpsdk.CallToolRequest, in appSinceInput) (*mcpsdk.CallToolResult, any, error) {
	if err := validateSince(in.Since); err != nil {
		return nil, nil, err
	}
	// The app page redirects to itself with a level when none is given, so
	// always name one.
	return s.readPage(ctx, req, appPath(in.Collector, in.App, ""), url.Values{"level": {"error"}}, in.Since)
}

func (s *Server) appStats(ctx context.Context, req *mcpsdk.CallToolRequest, in appSinceInput) (*mcpsdk.CallToolResult, any, error) {
	if err := validateSince(in.Since); err != nil {
		return nil, nil, err
	}
	return s.readPage(ctx, req, appPath(in.Collector, in.App, "/stats"), nil, in.Since)
}

func (s *Server) appUptime(ctx context.Context, req *mcpsdk.CallToolRequest, in appSinceInput) (*mcpsdk.CallToolResult, any, error) {
	if err := validateSince(in.Since); err != nil {
		return nil, nil, err
	}
	return s.readPage(ctx, req, appPath(in.Collector, in.App, "/uptime"), nil, in.Since)
}

func (s *Server) listLogGroups(ctx context.Context, req *mcpsdk.CallToolRequest, in listLogGroupsInput) (*mcpsdk.CallToolResult, any, error) {
	if err := validateSince(in.Since); err != nil {
		return nil, nil, err
	}
	level := in.Level
	if level == "" {
		level = "error"
	}
	if !validLevel[level] {
		return nil, nil, fmt.Errorf("invalid level %q: use one of all, error, warn, info", level)
	}
	if in.Offset < 0 {
		return nil, nil, fmt.Errorf("offset must not be negative, got %d", in.Offset)
	}
	return s.readPage(ctx, req, appPath(in.Collector, in.App, "/logs/groups"), pageQuery(level, in.Offset), in.Since)
}

func (s *Server) getLogGroup(ctx context.Context, req *mcpsdk.CallToolRequest, in logGroupInput) (*mcpsdk.CallToolResult, any, error) {
	if err := validateSince(in.Since); err != nil {
		return nil, nil, err
	}
	if in.GroupID <= 0 {
		return nil, nil, fmt.Errorf("group_id must be a positive number, got %d", in.GroupID)
	}
	// The group page renders its own summary (message, level, count, first and
	// latest) and links to the entries, so it is the whole answer.
	return s.readPage(ctx, req, groupPath(in.Collector, in.App, in.GroupID), nil, in.Since)
}

func (s *Server) logGroupEntries(ctx context.Context, req *mcpsdk.CallToolRequest, in logGroupEntriesInput) (*mcpsdk.CallToolResult, any, error) {
	if err := validateSince(in.Since); err != nil {
		return nil, nil, err
	}
	if in.GroupID <= 0 {
		return nil, nil, fmt.Errorf("group_id must be a positive number, got %d", in.GroupID)
	}
	if in.Offset < 0 {
		return nil, nil, fmt.Errorf("offset must not be negative, got %d", in.Offset)
	}
	return s.readPage(ctx, req, groupPath(in.Collector, in.App, in.GroupID)+"/entries", offsetQuery(in.Offset), in.Since)
}

func (s *Server) getLog(ctx context.Context, req *mcpsdk.CallToolRequest, in logInput) (*mcpsdk.CallToolResult, any, error) {
	if in.ID <= 0 {
		return nil, nil, fmt.Errorf("id must be a positive number, got %d", in.ID)
	}
	return s.readPage(ctx, req, appPath(in.Collector, in.App, "/logs/")+strconv.FormatInt(in.ID, 10), nil, "")
}

// ── Write tool handler ──────────────────────────────────────────────────────

func (s *Server) createApp(ctx context.Context, req *mcpsdk.CallToolRequest, in createAppInput) (*mcpsdk.CallToolResult, any, error) {
	if s.deps.NoAuth {
		return nil, nil, errors.New("creating apps is not allowed on this panel: it runs without authentication (demo mode)")
	}
	user, err := s.caller(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	allowed, err := s.canModify(ctx, user, in.Collector)
	if err != nil {
		return nil, nil, err
	}
	if !allowed {
		return nil, nil, fmt.Errorf("you are not allowed to add apps to collector %q", in.Collector)
	}

	name := strings.TrimSpace(in.Name)
	err = s.deps.Service.SaveApp(ctx, in.Collector, panelsvc.NewApp{
		Name:           name,
		Service:        strings.TrimSpace(in.Service),
		URL:            strings.TrimSpace(in.URL),
		Interval:       strings.TrimSpace(in.Interval),
		ExpectedStatus: strings.TrimSpace(in.ExpectedStatus),
	})
	switch {
	case errors.Is(err, panelsvc.ErrAppNameTaken):
		return nil, nil, fmt.Errorf("an app named %q already exists on collector %q", name, in.Collector)
	case errors.Is(err, panelsvc.ErrAppNoSource):
		return nil, nil, errors.New("an app needs a log source (service) or a health-check URL")
	case errors.Is(err, panelsvc.ErrAppSourceNotFound):
		return nil, nil, fmt.Errorf("the collector could not resolve that log source: %w", err)
	case errors.Is(err, panelsvc.ErrAppInvalidCheck):
		return nil, nil, fmt.Errorf("invalid health check: %w", err)
	case err != nil:
		return nil, nil, fmt.Errorf("could not create the app: %w", err)
	default:
		return textResult(fmt.Sprintf("Created app %q on collector %q. Read it with get_app, or pass a service or URL to monitor another.", name, in.Collector)), nil, nil
	}
}

// canModify reports whether the user may add apps to the collector. The
// service's SaveApp does not check permission itself, so the caller must.
func (s *Server) canModify(ctx context.Context, user domain.User, collector string) (bool, error) {
	if user.Role == domain.RoleAdmin {
		return true, nil
	}
	err := s.deps.Service.CanModifyCollector(ctx, user.ID, collector)
	if errors.Is(err, panelsvc.ErrCannotModifyCollector) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("could not check access to collector %q: %w", collector, err)
	}
	return true, nil
}

// ── Helpers ─────────────────────────────────────────────────────────────────

// appPath builds the path of an app resource; suffix is appended when set.
func appPath(collector, app, suffix string) string {
	return "/collectors/" + url.PathEscape(collector) + "/apps/" + url.PathEscape(app) + suffix
}

// groupPath builds the path of a log group resource.
func groupPath(collector, app string, groupID int64) string {
	return appPath(collector, app, "/logs/groups/") + strconv.FormatInt(groupID, 10)
}

// pageQuery builds the log-group filter query, omitting the offset when it is
// zero so the panel serves its first page.
func pageQuery(level string, offset int) url.Values {
	query := url.Values{"level": {level}}
	if offset > 0 {
		query.Set("offset", strconv.Itoa(offset))
	}
	return query
}

// offsetQuery builds a paging query, omitting a zero offset.
func offsetQuery(offset int) url.Values {
	query := url.Values{}
	if offset > 0 {
		query.Set("offset", strconv.Itoa(offset))
	}
	return query
}

// validateSince rejects a window the panel would silently replace with its own
// default, so the client learns it asked for something unsupported.
func validateSince(since string) error {
	if since == "" {
		return nil
	}
	if templates.SanitizeSinceLabel(since) == since {
		return nil
	}
	return fmt.Errorf("invalid since %q: use one of 1h, 6h, 24h, 7d, 30d", since)
}
