package forgejo

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"tracer/apps/panel/internal/services/notify"
)

// ownerRepoPattern matches Forgejo user/org and repository names.
// Requires at least one alphanumeric character to reject "." and "..".
var ownerRepoPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// Config holds the Forgejo connection settings and issue defaults.
//
// Trust model: the base_url and token are supplied by the authenticated panel
// user and used as-is. Any authenticated user can point base_url at an
// arbitrary host — this is intentional and consistent with the ntfy/telegram
// notifier model where credentials are the user's own. The token is sent via
// the Authorization header and never appears in URLs or logs; config values
// with secret keys are masked when rendered.
type Config struct {
	BaseURL   string  `json:"base_url"`
	Token     string  `json:"token"`
	Owner     string  `json:"owner"`
	Repo      string  `json:"repo"`
	Labels    []int64 `json:"labels,omitempty"` // label IDs to apply; empty = none
	Milestone int64   `json:"milestone,omitempty"`
}

// IssueRecorder persists the created issue back onto the log group.
type IssueRecorder interface {
	RecordGroupIssue(ctx context.Context, collectorName, appName string, groupID, issueNumber int64, issueURL string) error
}

// Issue mirrors the subset of the Forgejo Issue API response used here.
type Issue struct {
	Number  int64  `json:"number"`
	HTMLURL string `json:"html_url"`
}

// createIssueOption mirrors Forgejo's CreateIssueOption. Only title is required.
type createIssueOption struct {
	Title     string  `json:"title"`
	Body      string  `json:"body"`
	Labels    []int64 `json:"labels,omitempty"`
	Milestone int64   `json:"milestone,omitempty"`
}

// Notifier creates issues in a Forgejo repository.
type Notifier struct {
	name     string
	cfg      Config
	recorder IssueRecorder
	client   *Client
}

// New creates a forgejo notifier. The recorder persists created issues onto
// the log group rows.
func New(name, config string, recorder IssueRecorder) (*Notifier, error) {
	var cfg Config
	if err := json.Unmarshal([]byte(config), &cfg); err != nil {
		return nil, fmt.Errorf("could not parse forgejo config: %w", err)
	}

	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	cfg.Token = strings.TrimSpace(cfg.Token)
	cfg.Owner = strings.TrimSpace(cfg.Owner)
	cfg.Repo = strings.TrimSpace(cfg.Repo)

	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("base_url is required")
	}
	if cfg.Token == "" {
		return nil, fmt.Errorf("token is required")
	}
	if err := ValidCredentials(cfg.BaseURL, cfg.Token); err != nil {
		return nil, err
	}
	if err := ValidateOwnerRepo(cfg.Owner, cfg.Repo); err != nil {
		return nil, err
	}

	return &Notifier{
		name:     name,
		cfg:      cfg,
		recorder: recorder,
		client:   NewClient(cfg.BaseURL, cfg.Token),
	}, nil
}

// Type returns the notifier type.
func (n *Notifier) Type() string {
	return "forgejo"
}

// Name returns the user-defined name for this notifier instance.
func (n *Notifier) Name() string {
	return n.name
}

// Notify handles new error group events and welcome validation. Other
// notification types are ignored.
func (n *Notifier) Notify(notification notify.Notification) error {
	switch notification.Type {
	case notify.TypeNewErrorGroup:
		return n.createIssue(notification)
	case notify.TypeWelcome:
		return n.validateConnection()
	default:
		return nil
	}
}

func (n *Notifier) createIssue(notification notify.Notification) error {
	log, ok := notification.Payload.(notify.LogEntry)
	if !ok {
		return fmt.Errorf("could not create forgejo issue: expected LogEntry payload, got %T", notification.Payload)
	}

	first := firstLine(log.Message)
	if first == "" {
		first = "(no message)"
	}
	title := truncateBytes(fmt.Sprintf("[tracer] %s@%s: %s",
		notification.AppName, notification.CollectorName, first), 255)

	body := fmt.Sprintf("**App:** %s\n**Collector:** %s\n**Group ID:** %d\n\n```\n%s\n```",
		notification.AppName, notification.CollectorName, log.GroupID, log.Message)
	if log.LogURL != "" {
		body += fmt.Sprintf("\n\n[view log](%s)", log.LogURL)
	}

	// Create and record share one deadline so a slow instance cannot hold the
	// notification pipeline twice as long.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	issue, err := n.client.CreateIssue(ctx, n.cfg.Owner, n.cfg.Repo, title, body, n.cfg.Labels, n.cfg.Milestone)
	if err != nil {
		return err
	}

	if err := n.recorder.RecordGroupIssue(ctx,
		notification.CollectorName, notification.AppName, log.GroupID, issue.Number, issue.HTMLURL); err != nil {
		return fmt.Errorf("could not record forgejo issue on log group: %w", err)
	}

	return nil
}

// validateConnection confirms the token can read the configured repository.
func (n *Notifier) validateConnection() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := n.client.ValidateRepo(ctx, n.cfg.Owner, n.cfg.Repo); err != nil {
		return fmt.Errorf("%w: %w", notify.ErrWelcomeValidation, err)
	}
	return nil
}

// firstLine returns the first non-empty line of s, or "" when every line is
// blank.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

// truncateBytes limits s to at most n bytes without splitting a UTF-8
// sequence. Forgejo counts title length in characters/bytes, not runes, so a
// rune budget could overflow the limit for multi-byte titles.
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.ValidString(s[:cut]) {
		cut--
	}
	return s[:cut]
}
