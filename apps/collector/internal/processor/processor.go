package processor

import (
	"regexp"
	"time"

	"github.com/ai-shift/tracer/apps/collector/internal/config"
	"github.com/ai-shift/tracer/apps/collector/internal/parser"
	"github.com/ai-shift/tracer/libs/types"
)

// Processor parses raw lines and produces typed events.
type Processor struct {
	parser        parser.Parser
	appName       string
	startupRegex  *regexp.Regexp
	shutdownRegex *regexp.Regexp
}

// New creates a processor for an app.
func New(app config.ResolvedApp) (*Processor, error) {
	p, err := parser.New(app)
	if err != nil {
		return nil, err
	}

	var startupRegex, shutdownRegex *regexp.Regexp

	if app.StartupPattern != "" {
		startupRegex, err = regexp.Compile(app.StartupPattern)
		if err != nil {
			return nil, parser.ErrInvalidPattern
		}
	}

	if app.ShutdownPattern != "" {
		shutdownRegex, err = regexp.Compile(app.ShutdownPattern)
		if err != nil {
			return nil, parser.ErrInvalidPattern
		}
	}

	return &Processor{
		parser:        p,
		appName:       app.Name,
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
