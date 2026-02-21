package logx

import "log/slog"

// TODO: Migrate shared logging utilities here
func DefaultLogger() *slog.Logger {
	return slog.Default()
}
