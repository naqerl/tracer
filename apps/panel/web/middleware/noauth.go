package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"sync"

	usersvc "tracer/apps/panel/internal/services/user"
)

func BuildNoAuthMiddleware(userService *usersvc.Service) func(http.Handler) http.Handler {
	var (
		adminUserID int64
		once        sync.Once
	)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			once.Do(func() {
				adminUserID = resolveAdminUserID(userService)
				if adminUserID == 0 {
					once = sync.Once{}
				}
			})

			if adminUserID != 0 {
				r = SetUserID(r, adminUserID)
			}

			next.ServeHTTP(w, r)
		})
	}
}

func resolveAdminUserID(userService *usersvc.Service) int64 {
	users, err := userService.List(context.Background())
	if err != nil {
		slog.Error("no-auth middleware: failed to list users", "error", err)
		return 0
	}
	for _, u := range users {
		if u.IsAdmin == 1 {
			slog.Info("no-auth middleware: using admin user", "user_id", u.ID)
			return u.ID
		}
	}
	if len(users) > 0 {
		slog.Info("no-auth middleware: using first user (no admin found)", "user_id", users[0].ID)
		return users[0].ID
	}
	slog.Debug("no-auth middleware: no users yet, will retry")
	return 0
}
