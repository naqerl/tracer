package formatguesser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"tracer/apps/collector/internal/config"
	"tracer/apps/collector/internal/parser"
	"tracer/apps/collector/internal/parser/format"
	"tracer/apps/collector/internal/source"
)

func Add(ctx context.Context, cfg *config.Config, ss source.SourceSeeker) error {
	if err := ss.Open(ctx); err != nil {
		return fmt.Errorf("failed to open the source, error: %v", err)
	}
	
	logSize := int64(100)
	var logErr error
	found := false
	for !found && logSize > 1 {
		if _, logErr = ss.Seek(-logSize, io.SeekEnd); logErr != nil {
			return fmt.Errorf("failed to seek in the source, error: %v", logErr)
		}
		if _, logErr = ss.Next(ctx); logErr != nil {
			logSize = logSize / 2
			continue
		}
		found = true
	}
	if !found {
		return errors.New("failed to find logs in the source")
	}

	var prev parser.Spec
	var curr parser.Spec
	var formatErr error
	var line string
	for {
		line, formatErr  = ss.Next(ctx)
		if formatErr != nil {
			if errors.Is(formatErr, io.EOF) {
				break
			}
			return fmt.Errorf("unexpected error when reading the source, error: %v", formatErr)
		}
		curr, formatErr = guessFormat(line)
		if formatErr != nil {
			return fmt.Errorf("failed to guess the logs format, error: %v", formatErr)
		}
		prev, formatErr = compareOrUpdate(prev, curr)
		if formatErr != nil {
			return fmt.Errorf("failed to check the log consistency, error: %v", formatErr)
		}

	}

	return nil
}

func guessFormat(line string) (parser.Spec, error) {
	var ps parser.Spec

	var jsonData map[string]any
	err := json.Unmarshal([]byte(line), &jsonData)
	if err == nil {
		ps.Type = "json"
		return fillSpec(ps, jsonData)
	}
	slog.Debug("failed to unmarshal the line to JSON")
	tempSlogText, err := format.UnmarshalSlogText(line)
	if err == nil {
		ps.Type = "slogtext"
		slogTextData := make(map[string]any, len(tempSlogText))
		for k, v := range tempSlogText {
			slogTextData[k] = v
		}
		return fillSpec(ps, slogTextData)
	}
	slog.Debug("failed to unmarshal the line to SlogText")

	return ps, errors.New("none of the parsers gave a valid output")
}

var LogSynonyms = map[string][]string{
	"level_field": {
		"level", "lvl", "severity", "status", "priority", "log_level", "loglevel",
		"log.level", "sev", "s", "prio", "syslog_priority", "alert_level",
	},
	"trace_id_field": {
		"trace_id", "traceid", "traceId", "correlation_id", "correlationId",
		"request_id", "requestId", "req_id", "reqid", "tid", "x-trace-id",
		"trace.id", "otel.trace_id", "span_id", "spanid", "x-request-id",
		"x-b3-traceid", "operation_id", "guid", "uid",
	},
	"message_field": {
		"message", "msg", "body", "text", "content", "log_msg", "description",
		"summary", "payload", "log", "event_message", "error_message", "err_msg",
		"detail", "info", "raw", "data", "statement",
	},
	"timestamp_field": {
		"timestamp", "ts", "time", "@timestamp", "recorded_at", "created_at",
		"date", "time_stamp", "log_time", "event_time", "published_at",
		"syslog_timestamp", "start_time", "epoch", "unix_timestamp", "millis",
		"asctime",
	},
}

func fillSpec(ps parser.Spec, data map[string]any) (parser.Spec, error) {
	params := make(map[string]string)
	for dk := range data {
		for gk, gv := range LogSynonyms {
			if _, ok := params[gk]; ok {
				continue
			}
			if slices.Contains(gv, strings.ToLower(dk)) {
				params[gk] = dk
				continue
			}
		}
	}
	notFound := []string{}
	for k := range LogSynonyms {
		if _, ok := params[k]; !ok {
			notFound = append(notFound, k)
		}
	}
	if len(notFound) > 0 {
		return ps, fmt.Errorf("failed to find the following fields: %v", notFound)
	}

	ps.Params = params
	return ps, nil
}


func compareOrUpdate(prev, curr parser.Spec) (parser.Spec, error ) {
	if prev.Type == "" {
		prev.Type = curr.Type
	} else if prev.Type != curr.Type {
		return prev, fmt.Errorf("spec types mismatch. Prev: %s, Curr: %s", prev.Type, curr.Type)
	}

	if prev.Params == nil {
		prev.Params = make(map[string]string)
	}

	for k,currV := range curr.Params {
		prevV, ok := prev.Params[k]
		if !ok {
			prev.Params[k] = currV
		} else if prevV != currV {
			return prev, fmt.Errorf("spec params mismatch, param: %s. Prev: %s, Curr: %s", k, prevV, currV)
		}
	}

	return prev, nil
}
