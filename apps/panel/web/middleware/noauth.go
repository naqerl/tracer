package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"sync"

	"tracer/apps/panel/internal/domain"
	usersvc "tracer/apps/panel/internal/services/user"
)

func BuildNoAuthMiddleware(userService *usersvc.Service) func(http.Handler) http.Handler {
	var (
		mu          sync.Mutex
		adminUserID int64
	)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			if adminUserID == 0 {
				adminUserID = resolveAdminUserID(userService)
			}
			userID := adminUserID
			mu.Unlock()

			if userID != 0 {
				r = setNoAuthActor(r, userService, userID)
			}

			next.ServeHTTP(w, r)
		})
	}
}

// setNoAuthActor stores the resolved no-auth user as a full admin actor.
func setNoAuthActor(r *http.Request, userService *usersvc.Service, userID int64) *http.Request {
	user, err := userService.FindByID(r.Context(), domain.System{}, userID)
	if err != nil {
		slog.Error("could not resolve no-auth actor", "user_id", userID, "error", err)
		return r
	}
	return SetActor(r, user)
}

func resolveAdminUserID(userService *usersvc.Service) int64 {
	users, err := userService.List(context.Background(), domain.System{})
	if err != nil {
		slog.Error("no-auth middleware: failed to list users", "error", err)
		return 0
	}
	for _, u := range users {
		if u.Role == domain.RoleAdmin {
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
