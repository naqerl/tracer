package middleware

import (
	"context"
	"net/http"
)

const userContextKey markdownContextKey = "user"

func HasUserID(r *http.Request) bool {
	_, ok := r.Context().Value(userContextKey).(int64)
	return ok
}

func SetUserID(r *http.Request, userID int64) *http.Request {
	ctx := context.WithValue(r.Context(), userContextKey, userID)
	return r.WithContext(ctx)
}

func GetUserID(r *http.Request) (int64, bool) {
	userID, ok := r.Context().Value(userContextKey).(int64)
	return userID, ok
}
