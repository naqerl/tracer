package processor

import (
	"regexp"
	"time"

	"github.com/ai-shift/tracer/apps/collector/internal/parser"
	"github.com/ai-shift/tracer/libs/types"
)

// Config contains processor configuration
type Config struct {
	AppName         string
	StartupPattern  string
	ShutdownPattern string
}

// Processor parses raw lines and produces typed events.
type Processor struct {
	parser        parser.Parser
	appName       string
	startupRegex  *regexp.Regexp
	shutdownRegex *regexp.Regexp
}

// New creates a processor for an app.
func New(cfg Config, p parser.Parser) (*Processor, error) {
	var startupRegex, shutdownRegex *regexp.Regexp
	var err error

	if cfg.StartupPattern != "" {
		startupRegex, err = regexp.Compile(cfg.StartupPattern)
		if err != nil {
			return nil, parser.ErrInvalidPattern
		}
	}

	if cfg.ShutdownPattern != "" {
		shutdownRegex, err = regexp.Compile(cfg.ShutdownPattern)
		if err != nil {
			return nil, parser.ErrInvalidPattern
		}
	}

	return &Processor{
		parser:        p,
		appName:       cfg.AppName,
		startupRegex:  startupRegex,
		shutdownRegex: shutdownRegex,
	}, nil
}

// Process converts a raw line into an Event.
// Returns nil if parsing fails or line should be skipped.
func (p *Processor) Process(rl types.RawLine) types.Event {
	entry, err := p.parser.Parse(rl.Line)
	if err != nil {
		return nil
	}

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
