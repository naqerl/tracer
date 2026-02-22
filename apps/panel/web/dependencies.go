package web

import (
	"github.com/ai-shift/tracer/apps/panel/internal/dbmanager"
	"github.com/ai-shift/tracer/apps/panel/internal/services/auth"
	"github.com/ai-shift/tracer/apps/panel/internal/services/ingest"
	"github.com/ai-shift/tracer/apps/panel/internal/services/notify"
	"github.com/ai-shift/tracer/apps/panel/internal/services/user"
)

type Dependencies struct {
	DBManagerx *dbmanager.Manager
	Ingest     *ingest.Service
	Notify     *notify.Service
	Auth       *auth.Service
	User       *user.Service
	Tracker    *ingest.Tracker
}
