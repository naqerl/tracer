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

type Dependencies struct {
	DBManagerx      *dbmanager.Manager
	Ingest          *ingest.Service
	Notify          *notify.Service
	Auth            *auth.Service
	User            *user.Service
	Tracker         *ingest.Tracker
	ProgressTracker *ingest.UpdateProgressTracker
	Topology        *topologysvc.Service
	PanelService    *panelsvc.Service
	UpdateSvc       *updatesvc.Service
	ConnManager     *ingest.ConnManager
	Config          *config.Config
}
