package middleware

import (
	"context"
	"net/http"

	"tracer/apps/panel/internal/domain"
)

const userContextKey markdownContextKey = "user"

// GetUserID returns the authenticated user id from the request context.
func GetUserID(r *http.Request) (int64, bool) {
	actor, ok := GetActor(r)
	if !ok {
		return 0, false
	}
	user, ok := actor.(domain.User)
	return user.ID, ok
}

// HasUserID reports whether the request context carries an authenticated user.
func HasUserID(r *http.Request) bool {
	_, ok := GetUserID(r)
	return ok
}

// SetActor stores the caller actor in the request context.
func SetActor(r *http.Request, actor domain.Actor) *http.Request {
	ctx := context.WithValue(r.Context(), userContextKey, actor)
	return r.WithContext(ctx)
}

// GetActor returns the caller actor from the request context.
func GetActor(r *http.Request) (domain.Actor, bool) {
	actor, ok := r.Context().Value(userContextKey).(domain.Actor)
	return actor, ok
}
