package logparser

import (
	"encoding/json"
	"fmt"
	"regexp"

	collectorconfig "github.com/ai-shift/tracer/apps/collector/internal/config"
	"github.com/ai-shift/tracer/libs/types"
)

// Enricher parses log lines and adds collector-level metadata (app name, startup detection).
type Enricher struct {
	parser       Parser
	appName      string
	startupRegex *regexp.Regexp
	isJournalctl bool
}

// NewEnricher creates an Enricher from app configuration.
func NewEnricher(cfg collectorconfig.ResolvedApp) (*Enricher, error) {
	parser, err := New(cfg)
	if err != nil {
		return nil, err
	}

	e := &Enricher{
		parser:       parser,
		appName:      cfg.Name,
		isJournalctl: cfg.SourceKind == "journalctl",
	}

	if cfg.StartupPattern != "" {
		re, err := regexp.Compile(cfg.StartupPattern)
		if err != nil {
			return nil, ErrInvalidPattern
		}
		e.startupRegex = re
	}

	return e, nil
}

// Enrich parses a log line and returns a LogPayload with metadata.
// For journalctl sources, it extracts the MESSAGE field from the JSON wrapper.
func (e *Enricher) Enrich(line string) (types.LogPayload, error) {
	entry, err := e.parseWithFallback(line)
	if err != nil {
		return types.LogPayload{}, err
	}

	return types.LogPayload{
		AppName:   e.appName,
		Entry:     entry,
		IsStartup: e.isStartup(entry.Message),
	}, nil
}

// parseWithFallback attempts to parse the line. For journalctl sources,
// if parsing fails, it extracts the MESSAGE field and tries again.
func (e *Enricher) parseWithFallback(line string) (types.LogEntry, error) {
	entry, err := e.parser.Parse(line)
	if err == nil {
		return entry, nil
	}

	// For journalctl source, try extracting MESSAGE from JSON wrapper
	if e.isJournalctl {
		msg, extractErr := extractJournalMessage(line)
		if extractErr == nil {
			return e.parser.Parse(msg)
		}
	}

	return types.LogEntry{}, err
}

// extractJournalMessage extracts the MESSAGE field from journalctl JSON output.
func extractJournalMessage(line string) (string, error) {
	var data map[string]any
	if err := json.Unmarshal([]byte(line), &data); err != nil {
		return "", err
	}
	msg, ok := data["MESSAGE"]
	if !ok {
		return "", fmt.Errorf("missing MESSAGE field")
	}
	return fmt.Sprintf("%v", msg), nil
}

func (e *Enricher) isStartup(message string) bool {
	if e.startupRegex == nil {
		return false
	}
	return e.startupRegex.MatchString(message)
}

// AppName returns the app name this enricher is configured for.
func (e *Enricher) AppName() string {
	return e.appName
}
