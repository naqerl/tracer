package classify

import (
	"regexp"
	"time"

	"github.com/ai-shift/tracer/apps/collector/internal/config"
	"github.com/ai-shift/tracer/apps/collector/internal/parser"
	"github.com/ai-shift/tracer/libs/types"
)

// Classifier parses raw lines and classifies them into events.
type Classifier struct {
	parser        parser.Parser
	appName       string
	startupRegex  *regexp.Regexp
	shutdownRegex *regexp.Regexp
}

// New creates a classifier for an app.
func New(app config.ResolvedApp) (*Classifier, error) {
	p, err := parser.New(app)
	if err != nil {
		return nil, err
	}

	c := &Classifier{
		parser:  p,
		appName: app.Name,
	}

	if app.StartupPattern != "" {
		re, err := regexp.Compile(app.StartupPattern)
		if err != nil {
			return nil, parser.ErrInvalidPattern
		}
		c.startupRegex = re
	}

	if app.ShutdownPattern != "" {
		re, err := regexp.Compile(app.ShutdownPattern)
		if err != nil {
			return nil, parser.ErrInvalidPattern
		}
		c.shutdownRegex = re
	}

	return c, nil
}

// Process converts a raw line into an Event.
// Returns nil if parsing fails or line should be skipped.
func (c *Classifier) Process(rl types.RawLine) types.Event {
	entry, err := c.parser.Parse(rl.Line)
	if err != nil {
		return nil
	}

	msg := entry.Message
	ts := time.Now()

	if c.startupRegex != nil && c.startupRegex.MatchString(msg) {
		return types.StartupEvent{
			Time:    ts,
			App:     c.appName,
			Message: msg,
		}
	}

	if c.shutdownRegex != nil && c.shutdownRegex.MatchString(msg) {
		return types.ShutdownEvent{
			Time:   ts,
			App:    c.appName,
			Reason: msg,
		}
	}

	return types.LogEvent{
		Time:  ts,
		App:   c.appName,
		Entry: entry,
	}
}
