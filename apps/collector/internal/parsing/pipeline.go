package logparser

import (
	"regexp"
	"time"

	collectorconfig "github.com/ai-shift/tracer/apps/collector/internal/config"
	"github.com/ai-shift/tracer/libs/types"
)

// Pipeline parses raw lines and classifies them into events.
type Pipeline struct {
	parser        Parser
	appName       string
	startupRegex  *regexp.Regexp
	shutdownRegex *regexp.Regexp
	isJournalctl  bool
}

// NewPipeline creates a pipeline for an app.
func NewPipeline(cfg Config) (*Pipeline, error) {
	parser, err := New(cfg.ResolvedApp)
	if err != nil {
		return nil, err
	}

	p := &Pipeline{
		parser:       parser,
		appName:      cfg.Name,
		isJournalctl: cfg.SourceKind == "journalctl",
	}

	if cfg.StartupPattern != "" {
		re, err := regexp.Compile(cfg.StartupPattern)
		if err != nil {
			return nil, ErrInvalidPattern
		}
		p.startupRegex = re
	}

	if cfg.ShutdownPattern != "" {
		re, err := regexp.Compile(cfg.ShutdownPattern)
		if err != nil {
			return nil, ErrInvalidPattern
		}
		p.shutdownRegex = re
	}

	return p, nil
}

// Process converts a raw line into an Event.
// Returns nil if parsing fails or line should be skipped.
func (p *Pipeline) Process(rl types.RawLine) types.Event {
	line := rl.Line

	// For journalctl sources, extract MESSAGE from JSON wrapper
	if p.isJournalctl {
		if msg, err := extractJournalMessage(line); err == nil {
			line = msg
		}
	}

	entry, err := p.parser.Parse(line)
	if err != nil {
		return nil
	}

	// Classify event type
	msg := entry.Message
	ts := time.Now()

	if p.startupRegex != nil && p.startupRegex.MatchString(msg) {
		return types.StartupEvent{
			Time:    ts,
			App:     p.appName,
			Message: msg,
		}
	}

	if p.shutdownRegex != nil && p.shutdownRegex.MatchString(msg) {
		return types.ShutdownEvent{
			Time:   ts,
			App:    p.appName,
			Reason: msg,
		}
	}

	return types.LogEvent{
		Time:  ts,
		App:   p.appName,
		Entry: entry,
	}
}

// AppName returns the app this pipeline processes.
func (p *Pipeline) AppName() string {
	return p.appName
}

// Config holds pipeline configuration.
type Config struct {
	collectorconfig.ResolvedApp
	ShutdownPattern string
}
