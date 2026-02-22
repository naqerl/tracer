package web

import (
	"github.com/ai-shift/tracer/apps/panel/internal/services/auth"
	"github.com/ai-shift/tracer/apps/panel/internal/services/collectors"
	"github.com/ai-shift/tracer/apps/panel/internal/dbmanager"
	"github.com/ai-shift/tracer/apps/panel/internal/services/notify"
	"github.com/ai-shift/tracer/apps/panel/internal/services/user"        
)

type Dependencies struct {
        DBManagerx *dbmanager.Manager
        Collector *collectors.Service
        Notify *notify.Service
        Auth *auth.Service
        User *user.Service
        Tracker *collectors.Tracker
}
