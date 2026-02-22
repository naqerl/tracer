package web

import (
	"github.com/ai-shift/tracer/apps/panel/internal/auth"
	"github.com/ai-shift/tracer/apps/panel/internal/collectors"
	"github.com/ai-shift/tracer/apps/panel/internal/dbmanager"
	"github.com/ai-shift/tracer/apps/panel/internal/notify"
	"github.com/ai-shift/tracer/apps/panel/internal/user"        
)

type Dependencies struct {
        DBManagerx *dbmanager.Manager
        Collector *collectors.Service
        Notify *notify.Service
        Auth *auth.Service
        User *user.Service
        Tracker *collectors.Tracker
}
