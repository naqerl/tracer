package parser

import "errors"

var (
	ErrUnknownFormat  = errors.New("unknown log format")
	ErrInvalidPattern = errors.New("invalid regex pattern")
	ErrParseFailed    = errors.New("failed to parse log line")
)
