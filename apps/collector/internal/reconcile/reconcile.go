package reconcile

import (
	"context"
	"encoding/json"
	"fmt"

	"tracer/apps/collector/internal/app"
	"tracer/apps/collector/internal/health"
	"tracer/apps/collector/internal/lifecycle"
	"tracer/apps/collector/internal/parser"
	"tracer/apps/collector/internal/source"
	"tracer/apps/collector/internal/supervisor"
	"tracer/libs/types"
)

// Config drives supervision: the apps to run and the parsers they use.
// It is decoded from the panel's pushed CollectorConfig and adapted to
// collector-internal spec types.
type Config struct {
	Apps    []types.AppSpec
	Parsers map[string]parser.Spec
}

// Apply reconciles the running components against cfg and applies them to sup,
// hot-reloading only what changed.
func Apply(ctx context.Context, sup *supervisor.Supervisor, mpsc chan<- types.Event, cfg Config) error {
	// Validation is handled by CollectorConfig.Validate() before reaching here.

	// Build internal parser instances from the shared parser specs.
	parsers := make(map[string]parser.Parser, len(cfg.Parsers))
	for id, spec := range cfg.Parsers {
		spec.ID = id
		parsers[id] = parser.New(spec)
	}

	// Build supervisor components. Capacity is len*2 because most apps
	// produce both a log component and a health component; health-only
	// apps contribute just one.
	components := make([]supervisor.Component, 0, len(cfg.Apps)*2)
	for _, spec := range cfg.Apps {
		if !spec.IsHealthOnly() {
			p, ok := parsers[spec.ParserID]
			if !ok {
				return fmt.Errorf("parser with id=%s not found", spec.ParserID)
			}
			s, err := source.New(source.Spec{Type: spec.Source.Type, Params: spec.Source.Params})
			if err != nil {
				return fmt.Errorf("invalid source config for app %s: %w", spec.Name, err)
			}
			var lfc lifecycle.Detector
			if lfcSpec, ok := spec.LifecycleDetectorSpec(); ok {
				lfc, err = lifecycle.New(*lfcSpec)
				if err != nil {
					return fmt.Errorf("invalid lifecycle config for app %s: %w", spec.Name, err)
				}
			}

			components = append(components, appComponent{
				id:   "app:" + spec.Name,
				sig:  componentSignature(spec, cfg.Parsers[spec.ParserID]),
				app:  app.New(s, p, lfc, spec.Name),
				mpsc: mpsc,
			})
		}

		if healthSpec, ok := spec.HealthSpec(); ok {
			poller, err := health.New(*healthSpec)
			if err != nil {
				return fmt.Errorf("invalid health config for app %s: %w", spec.Name, err)
			}
			sig := componentSignature(spec, parser.Spec{})
			if spec.IsHealthOnly() {
				sig = "health-only:" + spec.Name + ":" + sig
			}
			components = append(components, healthComponent{
				id:     "health:" + spec.Name,
				sig:    sig,
				app:    spec.Name,
				poller: poller,
				mpsc:   mpsc,
			})
		}
	}

	sup.Apply(ctx, components)
	return nil
}

// Names returns the configured app names in a stable order.
func Names(cfg Config) []string {
	names := make([]string, 0, len(cfg.Apps))
	for _, a := range cfg.Apps {
		names = append(names, a.Name)
	}
	return names
}

// componentSignature produces an opaque string that changes when any
// config-relevant field of the app or its parser changes. The supervisor
// uses it to decide whether a component needs restarting.
func componentSignature(appSpec types.AppSpec, parserSpec parser.Spec) string {
	b, err := json.Marshal(struct {
		App    types.AppSpec `json:"app"`
		Parser parser.Spec   `json:"parser"`
	}{appSpec, parserSpec})
	if err != nil {
		return appSpec.Name
	}
	return string(b)
}

// ── Supervisor components ───────────────────────────────────────────────────

type appComponent struct {
	id   string
	sig  string
	app  app.App
	mpsc chan<- types.Event
}

func (c appComponent) ID() string        { return c.id }
func (c appComponent) Signature() string { return c.sig }
func (c appComponent) Run(ctx context.Context) {
	for evt := range c.app.Produce(ctx) {
		select {
		case c.mpsc <- evt:
		case <-ctx.Done():
			return
		}
	}
}

type healthComponent struct {
	id     string
	sig    string
	app    string
	poller health.Poller
	mpsc   chan<- types.Event
}

func (c healthComponent) ID() string        { return c.id }
func (c healthComponent) Signature() string { return c.sig }
func (c healthComponent) Run(ctx context.Context) {
	for e := range c.poller.Produce(ctx) {
		select {
		case c.mpsc <- types.HealthEvent{
			App:    c.app,
			Time:   e.Time,
			Status: e.Status,
		}:
		case <-ctx.Done():
			return
		}
	}
}
