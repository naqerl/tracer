package web

import (
	"tracer/apps/panel/internal/dbmanager"
	"tracer/apps/panel/internal/services/auth"
	"tracer/apps/panel/internal/services/ingest"
	"tracer/apps/panel/internal/services/notify"
	"tracer/apps/panel/internal/services/user"
)

type Dependencies struct {
	DBManagerx *dbmanager.Manager
	Ingest     *ingest.Service
	Notify     *notify.Service
	Auth       *auth.Service
	User       *user.Service
	Tracker    *ingest.Tracker
}
