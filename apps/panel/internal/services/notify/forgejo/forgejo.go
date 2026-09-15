package forgejo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"text/template"
	"time"
	"unicode/utf8"

	"tracer/apps/panel/internal/services/notify"
)

//go:embed issue.tmpl
var issueTemplatesFS embed.FS

// issueTemplates renders the title and body of created issues. Parsed once at
// package init; a broken template fails the build.
var issueTemplates = template.Must(template.New("issue").ParseFS(issueTemplatesFS, "issue.tmpl"))

// ownerRepoPattern matches Forgejo user/org and repository names.
// Requires at least one alphanumeric character to reject "." and "..".
var ownerRepoPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// IssueCacheTTL is how long a reported message hash is remembered before the
// notifier consults Forgejo again. It covers Forgejo's search-index lag after
// issue creation (~1-3s) with margin, while staying short enough that a
// manually closed issue re-reports promptly on its next occurrence.
const IssueCacheTTL = time.Minute

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

// Issue mirrors the subset of the Forgejo Issue API response used here.
type Issue struct {
	Number  int64  `json:"number"`
	HTMLURL string `json:"html_url"`
	State   string `json:"state"`
}

// createIssueOption mirrors Forgejo's CreateIssueOption. Only title is required.
type createIssueOption struct {
	Title     string  `json:"title"`
	Body      string  `json:"body"`
	Labels    []int64 `json:"labels,omitempty"`
	Milestone int64   `json:"milestone,omitempty"`
}

// Notifier creates issues in a Forgejo repository. Forgejo is the source of
// truth for whether an error was already reported: the issue title carries the
// first line of the log message, and creation is skipped when an issue with
// that first line already exists (a closed issue gets a re-occurrence comment
// instead of a duplicate).
type Notifier struct {
	name   string
	cfg    Config
	client *Client
	cache  *Cache
}

// New creates a forgejo notifier. cache remembers recently reported hashes so
// repeated errors skip the search while Forgejo's index catches up; it is a
// required dependency (a nil cache would panic on first use).
func New(name, config string, cache *Cache) (*Notifier, error) {
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
	if cache == nil {
		return nil, fmt.Errorf("cache is required")
	}

	return &Notifier{
		name:   name,
		cfg:    cfg,
		client: NewClient(cfg.BaseURL, cfg.Token),
		cache:  cache,
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

// Notify handles error alerts and welcome validation. Other notification
// types are ignored.
func (n *Notifier) Notify(notification notify.Notification) error {
	switch notification.Type {
	case notify.TypeAlert:
		return n.createIssue(notification)
	case notify.TypeWelcome:
		return n.validateConnection()
	default:
		return nil
	}
}

// issueData is the data rendered into the issue title and body templates.
type issueData struct {
	Hash      string
	App       string
	Collector string
	GroupID   int64
	FirstLine string
	Message   string
	LogURL    string
}

func (n *Notifier) createIssue(notification notify.Notification) error {
	log, ok := notification.Payload.(notify.LogEntry)
	if !ok {
		return fmt.Errorf("could not create forgejo issue: expected LogEntry payload, got %T", notification.Payload)
	}

	hash := messageHash(log.Message)
	// Dedup keys and the issue title on the same truncated first line so a
	// title cut at 255 bytes is still found by the search and cache.
	first := truncateBytes(firstLine(log.Message), 255)
	if first == "" {
		return fmt.Errorf("could not create forgejo issue: empty log message")
	}
	data := issueData{
		Hash:      hash,
		App:       notification.AppName,
		Collector: notification.CollectorName,
		GroupID:   log.GroupID,
		FirstLine: first,
		Message:   log.Message,
		LogURL:    log.LogURL,
	}

	var bodyBuf bytes.Buffer
	if err := issueTemplates.ExecuteTemplate(&bodyBuf, "body", data); err != nil {
		return fmt.Errorf("could not render forgejo issue body: %w", err)
	}
	body := bodyBuf.String()

	var titleBuf bytes.Buffer
	if err := issueTemplates.ExecuteTemplate(&titleBuf, "title", data); err != nil {
		return fmt.Errorf("could not render forgejo issue title: %w", err)
	}
	title := truncateBytes(titleBuf.String(), 255)

	// Create and search share one deadline so a slow instance cannot hold the
	// notification pipeline twice as long.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Recently reported first lines skip the search: Forgejo's index lags behind
	// creation, so a fresh issue would not be found and would be duplicated.
	// Forgejo stays the long-term source of truth; the cache only covers the
	// lag window and doubles as an in-flight guard for concurrent alerts.
	if n.cache.Check(n.cfg.BaseURL, n.cfg.Owner, n.cfg.Repo, first) {
		return nil
	}

	// Reserve the first line before the network calls so concurrent identical
	// alerts during the search/create round-trip do not both create an issue.
	n.cache.Remember(n.cfg.BaseURL, n.cfg.Owner, n.cfg.Repo, first)

	// Forgejo is the source of truth for the first report: when an issue with
	// this first line already exists, the error was already reported. A closed
	// issue gets a re-occurrence comment on the original report instead of a
	// duplicate issue.
	issue, found, err := n.client.FindIssueByTitle(ctx, n.cfg.Owner, n.cfg.Repo, first)
	if err != nil {
		n.cache.Forget(n.cfg.BaseURL, n.cfg.Owner, n.cfg.Repo, first)
		return err
	}
	if found {
		if issue.State == "closed" {
			var commentBuf bytes.Buffer
			if err := issueTemplates.ExecuteTemplate(&commentBuf, "comment", data); err != nil {
				return fmt.Errorf("could not render forgejo re-occurrence comment: %w", err)
			}
			if err := n.client.CreateComment(ctx, n.cfg.Owner, n.cfg.Repo, issue.Number, commentBuf.String()); err != nil {
				return err
			}
		}
		return nil
	}

	if _, err := n.client.CreateIssue(ctx, n.cfg.Owner, n.cfg.Repo, title, body, n.cfg.Labels, n.cfg.Milestone); err != nil {
		// Nothing was created; drop the reservation so the next alert retries.
		n.cache.Forget(n.cfg.BaseURL, n.cfg.Owner, n.cfg.Repo, first)
		return err
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

// base62Chars is the digit alphabet used by messageHash, ordered 0-9a-zA-Z
// so the encoded value sorts like the reference Python implementation.
const base62Chars = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

// messageHash returns a 10-character base62 digest of the log message, used
// as the dedup key in the issue title. It matches the tracer reference
// implementation byte-for-byte so existing issues keep their hash across
// upgrades.
func messageHash(message string) string {
	sum := sha256.Sum256([]byte(message))
	num := new(big.Int).SetBytes(sum[:])

	// Collect the 10 least-significant base62 digits, least significant
	// first, exactly like the reference implementation.
	digits := make([]byte, 0, 10)
	for i := 0; i < 10 && num.Sign() > 0; i++ {
		var remainder big.Int
		num.QuoRem(num, big.NewInt(62), &remainder)
		digits = append(digits, base62Chars[remainder.Int64()])
	}

	// zfill(10): left-pad with '0' so the result is always 10 characters.
	for len(digits) < 10 {
		digits = append([]byte{'0'}, digits...)
	}
	return string(digits)
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
