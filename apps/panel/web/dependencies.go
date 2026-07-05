package web

import (
	"tracer/apps/panel/internal/config"
	"tracer/apps/panel/internal/dbmanager"
	"tracer/apps/panel/internal/services/auth"
	"tracer/apps/panel/internal/services/ingest"
	"tracer/apps/panel/internal/services/notify"
	panelsvc "tracer/apps/panel/internal/services/panel"
	topologysvc "tracer/apps/panel/internal/services/topology"
	updatesvc "tracer/apps/panel/internal/services/update"
	"tracer/apps/panel/internal/services/user"
)

// Dependencies holds all shared services injected into web handlers.
type Dependencies struct {
	DBManager *dbmanager.Manager // db schema and migration management

	// Services
	Ingest   *ingest.Service
	Notify   *notify.Service
	Auth     *auth.Service
	User     *user.Service
	Topology *topologysvc.Service
	Panel    *panelsvc.Service // dashboard UI
	Update   *updatesvc.Service
	Config   *config.Config

	// Tracker tracks connected collectors (WebSocket peers).
	Tracker *ingest.Tracker
	// UpdateTracker tracks per-entity update progress (download → build → … → complete).
	UpdateTracker *ingest.UpdateProgressTracker
	// ConnManager manages WebSocket connections to collectors.
	ConnManager *ingest.ConnManager
}
