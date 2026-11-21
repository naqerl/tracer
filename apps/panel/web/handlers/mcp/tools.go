package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"tracer/apps/panel/internal/domain"
	notifysvc "tracer/apps/panel/internal/services/notify"
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
	HealthType     string `json:"health_type,omitempty" jsonschema:"health check type: http or command (default http)"`
	URL            string `json:"url,omitempty" jsonschema:"optional HTTP health-check URL to poll"`
	Command        string `json:"command,omitempty" jsonschema:"shell command to run for a command health check (via /bin/sh -c)"`
	Expect         string `json:"expect,omitempty" jsonschema:"Go regular expression the command output must match"`
	ExpectedExit   string `json:"expected_exit,omitempty" jsonschema:"comma-separated exit codes that count as up, e.g. 0,2 (default 0)"`
	Interval       string `json:"interval,omitempty" jsonschema:"how often to run the health check, e.g. 30s (default 30s)"`
	ExpectedStatus string `json:"expected_status,omitempty" jsonschema:"comma-separated HTTP status codes that count as up, e.g. 200,301 (default any 2xx)"`
	AlertDelay     string `json:"alert_delay,omitempty" jsonschema:"how long the app must stay down before an uptime alert is sent, e.g. 5m (default 0s: alert immediately)"`
}

type createNotifierInput struct {
	Type      string `json:"type" jsonschema:"notifier type: ntfy, telegram or forgejo"`
	Name      string `json:"name" jsonschema:"name for the new notifier"`
	URL       string `json:"url,omitempty" jsonschema:"ntfy server URL (default https://ntfy.sh)"`
	Topic     string `json:"topic,omitempty" jsonschema:"ntfy topic name"`
	Token     string `json:"token,omitempty" jsonschema:"telegram bot token or Forgejo API token"`
	ChatID    int64  `json:"chat_id,omitempty" jsonschema:"telegram chat id"`
	BaseURL   string `json:"base_url,omitempty" jsonschema:"Forgejo base URL"`
	OwnerRepo string `json:"owner_repo,omitempty" jsonschema:"Forgejo repository as owner/repo"`
	Labels    string `json:"labels,omitempty" jsonschema:"Forgejo labels: comma-separated label names (ntfy/telegram unused)"`
	Milestone string `json:"milestone,omitempty" jsonschema:"Forgejo milestone name"`
	Config    string `json:"config,omitempty" jsonschema:"raw notifier config JSON; when set, the type-specific fields are ignored"`
}

type notifierIDInput struct {
	NotifierID int64 `json:"notifier_id" jsonschema:"notifier id, as returned by create_notifier"`
}

type notifierSubscriptionInput struct {
	NotifierID int64  `json:"notifier_id" jsonschema:"notifier id, as returned by create_notifier"`
	Collector  string `json:"collector" jsonschema:"collector name"`
	App        string `json:"app" jsonschema:"app name"`
}

// ── Tool registration ───────────────────────────────────────────────────────

func (s *Server) registerTools() {
	addTool(s.server, readOnlyTool("list_collectors",
		"List every collector the caller can see, with connection status, system stats and app counts. Start here to find collector names."),
		s.listCollectors)
	addTool(s.server, readOnlyTool("get_collector",
		"Show one collector's overview: connection state, CPU/RAM/disk snapshot and how many apps it has."),
		s.getCollector)
	addTool(s.server, readOnlyTool("collector_resources",
		"CPU, RAM and disk history for one collector over a time window."),
		s.collectorResources)
	addTool(s.server, readOnlyTool("list_apps",
		"List the apps on one collector with uptime bars, error counts and the error trend for the window, so the failing apps stand out."),
		s.listApps)
	addTool(s.server, readOnlyTool("get_app",
		"Show one app's page: its uptime for the window, with links to its log stats and log groups. Call list_log_groups for the app's error groups and app_stats for the log counts."),
		s.getApp)
	addTool(s.server, readOnlyTool("app_stats",
		"Log volume for one app over the window: totals and the error, warning and info split."),
		s.appStats)
	addTool(s.server, readOnlyTool("app_uptime",
		"Uptime percentage and per-bucket bars for one app over the window."),
		s.appUptime)
	addTool(s.server, readOnlyTool("list_log_groups",
		"Grouped, deduplicated log messages for one app, newest first, with an occurrence count per group. This is where failures show up; pass level=all to include info and warnings."),
		s.listLogGroups)
	addTool(s.server, readOnlyTool("get_log_group",
		"One log group in full: its message, level and count. Use log_group_entries for the individual entries."),
		s.getLogGroup)
	addTool(s.server, readOnlyTool("log_group_entries",
		"Individual log entries inside one group, each with its timestamp and context. Use offset to page; the panel returns 50 per page."),
		s.logGroupEntries)
	addTool(s.server, readOnlyTool("get_log",
		"One log entry in full: message, level, time, trace id and structured context."),
		s.getLog)
	addTool(s.server, createAppTool(),
		s.createApp)
	addTool(s.server, createNotifierTool(),
		s.createNotifier)
	addTool(s.server, listNotifierSubscriptionsTool(),
		s.listNotifierSubscriptions)
	addTool(s.server, addNotifierSubscriptionTool(),
		s.addNotifierSubscription)
	addTool(s.server, removeNotifierSubscriptionTool(),
		s.removeNotifierSubscription)
	addTool(s.server, deleteNotifierTool(),
		s.deleteNotifier)
}

// addTool registers a tool and logs every call to it.
func addTool[In any](server *mcpsdk.Server, tool *mcpsdk.Tool, handler mcpsdk.ToolHandlerFor[In, any]) {
	mcpsdk.AddTool(server, tool, withLogging(tool.Name, handler))
}

// withLogging wraps a tool handler so every call is logged with its name,
// arguments, duration and outcome. This is what lets the panel log show which
// tool an agent asked for, not just that /mcp was called.
func withLogging[In any](name string, handler mcpsdk.ToolHandlerFor[In, any]) mcpsdk.ToolHandlerFor[In, any] {
	return func(ctx context.Context, req *mcpsdk.CallToolRequest, in In) (*mcpsdk.CallToolResult, any, error) {
		start := time.Now()
		res, out, err := handler(ctx, req, in)

		attrs := []any{"tool", name, "duration_ms", time.Since(start).Milliseconds()}
		args, mErr := json.Marshal(in)
		if mErr != nil {
			slog.Error("could not marshal tool arguments for logging", "tool", name, "error", mErr)
		} else {
			attrs = append(attrs, "args", string(args))
		}
		if err != nil {
			attrs = append(attrs, "isError", true, "error", err.Error())
		}
		slog.Info("MCP tool call", attrs...)
		return res, out, err
	}
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
			"or a health check to watch it, or both. health_type=http probes a URL; health_type=command runs a shell command " +
			"and can require an output regexp (expect) and one or more expected_exit codes. The collector resolves the log " +
			"format when a service is given. Not available on a panel that runs without authentication (demo mode), and " +
			"requires permission to modify the collector.",
		Annotations: &mcpsdk.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &destructive},
	}
}

// createNotifierTool returns the notifier-creation tool.
func createNotifierTool() *mcpsdk.Tool {
	destructive := false
	return &mcpsdk.Tool{
		Name: "create_notifier",
		Description: "Create a notifier (ntfy, telegram or forgejo) for the caller. " +
			"For ntfy pass url and topic; for telegram pass token and chat_id; for forgejo pass base_url, token and owner_repo. " +
			"Or pass a raw config JSON via config. The notifier is tested (welcome notification) before it is saved.",
		Annotations: &mcpsdk.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &destructive},
	}
}

func listNotifierSubscriptionsTool() *mcpsdk.Tool {
	destructive := false
	return &mcpsdk.Tool{
		Name:        "list_notifier_subscriptions",
		Description: "List the collector/app subscriptions of a notifier.",
		Annotations: &mcpsdk.ToolAnnotations{ReadOnlyHint: true, DestructiveHint: &destructive},
	}
}
func addNotifierSubscriptionTool() *mcpsdk.Tool {
	destructive := false
	return &mcpsdk.Tool{
		Name:        "add_notifier_subscription",
		Description: "Subscribe a notifier to one collector/app pair (append; does not remove existing subscriptions).",
		Annotations: &mcpsdk.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &destructive},
	}
}
func removeNotifierSubscriptionTool() *mcpsdk.Tool {
	destructive := false
	return &mcpsdk.Tool{
		Name:        "remove_notifier_subscription",
		Description: "Unsubscribe a notifier from one collector/app pair.",
		Annotations: &mcpsdk.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &destructive},
	}
}

func deleteNotifierTool() *mcpsdk.Tool {
	destructive := true
	return &mcpsdk.Tool{
		Name:        "delete_notifier",
		Description: "Delete a notifier by id.",
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
		HealthType:     strings.TrimSpace(in.HealthType),
		URL:            strings.TrimSpace(in.URL),
		Command:        strings.TrimSpace(in.Command),
		Expect:         strings.TrimSpace(in.Expect),
		ExpectedExit:   strings.TrimSpace(in.ExpectedExit),
		Interval:       strings.TrimSpace(in.Interval),
		ExpectedStatus: strings.TrimSpace(in.ExpectedStatus),
		AlertDelay:     strings.TrimSpace(in.AlertDelay),
	})
	switch {
	case errors.Is(err, panelsvc.ErrAppNameTaken):
		return nil, nil, fmt.Errorf("an app named %q already exists on collector %q", name, in.Collector)
	case errors.Is(err, panelsvc.ErrAppNoSource):
		return nil, nil, errors.New("an app needs a log source (service) or a health check")
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

// createNotifier creates a notifier for the caller.
func (s *Server) createNotifier(ctx context.Context, req *mcpsdk.CallToolRequest, in createNotifierInput) (*mcpsdk.CallToolResult, any, error) {
	if s.deps.NoAuth {
		return nil, nil, errors.New("creating notifiers is not allowed on this panel: it runs without authentication (demo mode)")
	}
	if s.deps.Notify == nil {
		return nil, nil, errors.New("notifier service is not configured on this panel")
	}
	user, err := s.caller(ctx, req)
	if err != nil {
		return nil, nil, err
	}

	notifierType := strings.TrimSpace(in.Type)
	if notifierType == "" {
		return nil, nil, errors.New("type is required")
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, nil, errors.New("name is required")
	}

	configJSON := strings.TrimSpace(in.Config)
	if configJSON == "" {
		switch notifierType {
		case "ntfy":
			url := strings.TrimSpace(in.URL)
			if url == "" {
				url = "https://ntfy.sh"
			}
			topic := strings.TrimSpace(in.Topic)
			if topic == "" {
				return nil, nil, errors.New("topic is required for ntfy notifiers")
			}
			cfg := struct {
				URL   string `json:"url"`
				Topic string `json:"topic"`
			}{URL: url, Topic: topic}
			b, err := json.Marshal(cfg)
			if err != nil {
				return nil, nil, fmt.Errorf("could not marshal ntfy config: %w", err)
			}
			configJSON = string(b)
		case "telegram":
			token := strings.TrimSpace(in.Token)
			chatID := in.ChatID
			if token == "" || chatID == 0 {
				return nil, nil, errors.New("token and chat_id are required for telegram notifiers")
			}
			cfg := struct {
				Token  string `json:"token"`
				ChatID int64  `json:"chat_id"`
			}{Token: token, ChatID: chatID}
			b, err := json.Marshal(cfg)
			if err != nil {
				return nil, nil, fmt.Errorf("could not marshal telegram config: %w", err)
			}
			configJSON = string(b)
		case "forgejo":
			baseURL := strings.TrimRight(strings.TrimSpace(in.BaseURL), "/")
			token := strings.TrimSpace(in.Token)
			ownerRepo := strings.TrimSpace(in.OwnerRepo)
			if baseURL == "" || token == "" || ownerRepo == "" {
				return nil, nil, errors.New("base_url, token and owner_repo are required for forgejo notifiers")
			}
			parts := strings.SplitN(ownerRepo, "/", 2)
			if len(parts) != 2 {
				return nil, nil, fmt.Errorf("owner_repo must be owner/repo, got %q", ownerRepo)
			}
			owner := strings.TrimSpace(parts[0])
			repo := strings.TrimSpace(parts[1])
			if owner == "" || repo == "" {
				return nil, nil, fmt.Errorf("owner_repo must be owner/repo, got %q", ownerRepo)
			}
			cfg := struct {
				BaseURL   string  `json:"base_url"`
				Token     string  `json:"token"`
				Owner     string  `json:"owner"`
				Repo      string  `json:"repo"`
				Labels    []int64 `json:"labels,omitempty"`
				Milestone int64   `json:"milestone,omitempty"`
			}{BaseURL: baseURL, Token: token, Owner: owner, Repo: repo}
			b, err := json.Marshal(cfg)
			if err != nil {
				return nil, nil, fmt.Errorf("could not marshal forgejo config: %w", err)
			}
			configJSON = string(b)
		default:
			return nil, nil, fmt.Errorf("unknown notifier type %q", notifierType)
		}
	}

	req2 := notifysvc.AddNotifierRequest{
		UserID: user.ID,
		Name:   name,
		Type:   notifierType,
		Config: configJSON,
	}
	if err := req2.Validate(); err != nil {
		return nil, nil, err
	}

	id, err := s.deps.Notify.Add(ctx, user, req2)
	switch {
	case errors.Is(err, notifysvc.ErrWelcomeValidation):
		return nil, nil, fmt.Errorf("welcome validation failed: %w", err)
	case err != nil:
		return nil, nil, fmt.Errorf("could not create the notifier: %w", err)
	default:
		return textResult(fmt.Sprintf("Created notifier %q (id %d) of type %q.", name, id, notifierType)), nil, nil
	}
}

func (s *Server) listNotifierSubscriptions(ctx context.Context, req *mcpsdk.CallToolRequest, in notifierIDInput) (*mcpsdk.CallToolResult, any, error) {
	if s.deps.Notify == nil {
		return nil, nil, errors.New("notifier service is not configured on this panel")
	}
	user, err := s.caller(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	subs, err := s.deps.Notify.GetSubscriptions(ctx, user, in.NotifierID)
	if err != nil {
		return nil, nil, fmt.Errorf("could not list subscriptions: %w", err)
	}
	if len(subs) == 0 {
		return textResult("No subscriptions."), nil, nil
	}
	var b strings.Builder
	for _, sub := range subs {
		fmt.Fprintf(&b, "%s/%s\n", sub.CollectorName, sub.AppName)
	}
	return textResult(strings.TrimSpace(b.String())), nil, nil
}

func (s *Server) addNotifierSubscription(ctx context.Context, req *mcpsdk.CallToolRequest, in notifierSubscriptionInput) (*mcpsdk.CallToolResult, any, error) {
	if s.deps.NoAuth {
		return nil, nil, errors.New("modifying notifiers is not allowed on this panel: it runs without authentication (demo mode)")
	}
	if s.deps.Notify == nil {
		return nil, nil, errors.New("notifier service is not configured on this panel")
	}
	user, err := s.caller(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	subs, err := s.deps.Notify.GetSubscriptions(ctx, user, in.NotifierID)
	if err != nil {
		return nil, nil, fmt.Errorf("could not load subscriptions: %w", err)
	}
	for _, sub := range subs {
		if sub.CollectorName == in.Collector && sub.AppName == in.App {
			return textResult(fmt.Sprintf("Notifier %d is already subscribed to %s/%s.", in.NotifierID, in.Collector, in.App)), nil, nil
		}
	}
	subs = append(subs, notifysvc.Subscription{CollectorName: in.Collector, AppName: in.App})
	if err := s.deps.Notify.SetSubscriptions(ctx, user, in.NotifierID, subs); err != nil {
		return nil, nil, fmt.Errorf("could not add subscription: %w", err)
	}
	return textResult(fmt.Sprintf("Subscribed notifier %d to %s/%s.", in.NotifierID, in.Collector, in.App)), nil, nil
}

func (s *Server) removeNotifierSubscription(ctx context.Context, req *mcpsdk.CallToolRequest, in notifierSubscriptionInput) (*mcpsdk.CallToolResult, any, error) {
	if s.deps.NoAuth {
		return nil, nil, errors.New("modifying notifiers is not allowed on this panel: it runs without authentication (demo mode)")
	}
	if s.deps.Notify == nil {
		return nil, nil, errors.New("notifier service is not configured on this panel")
	}
	user, err := s.caller(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	subs, err := s.deps.Notify.GetSubscriptions(ctx, user, in.NotifierID)
	if err != nil {
		return nil, nil, fmt.Errorf("could not load subscriptions: %w", err)
	}
	filtered := subs[:0]
	for _, sub := range subs {
		if sub.CollectorName == in.Collector && sub.AppName == in.App {
			continue
		}
		filtered = append(filtered, sub)
	}
	if len(filtered) == len(subs) {
		return textResult(fmt.Sprintf("Notifier %d was not subscribed to %s/%s.", in.NotifierID, in.Collector, in.App)), nil, nil
	}
	if err := s.deps.Notify.SetSubscriptions(ctx, user, in.NotifierID, filtered); err != nil {
		return nil, nil, fmt.Errorf("could not remove subscription: %w", err)
	}
	return textResult(fmt.Sprintf("Unsubscribed notifier %d from %s/%s.", in.NotifierID, in.Collector, in.App)), nil, nil
}

func (s *Server) deleteNotifier(ctx context.Context, req *mcpsdk.CallToolRequest, in notifierIDInput) (*mcpsdk.CallToolResult, any, error) {
	if s.deps.NoAuth {
		return nil, nil, errors.New("deleting notifiers is not allowed on this panel: it runs without authentication (demo mode)")
	}
	if s.deps.Notify == nil {
		return nil, nil, errors.New("notifier service is not configured on this panel")
	}
	user, err := s.caller(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	if err := s.deps.Notify.Delete(ctx, user, in.NotifierID); err != nil {
		return nil, nil, fmt.Errorf("could not delete notifier: %w", err)
	}
	return textResult(fmt.Sprintf("Deleted notifier %d.", in.NotifierID)), nil, nil
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
