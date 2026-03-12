package formatguesser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"tracer/apps/collector/internal/config"
	"tracer/apps/collector/internal/parser"
	"tracer/apps/collector/internal/parser/format"
	"tracer/apps/collector/internal/source"
)

func Add(ctx context.Context, cfg *config.Config, name string, spec source.Spec, override bool) error {
	var app config.AppSpec

	appNames := make([]string, 0, len(cfg.Apps))
	for _, v := range cfg.Apps {
		appNames = append(appNames, v.Name)
	}
	app.Name = generateName(appNames, name)

	// Check if app with such spec already exist
	for _, a := range cfg.Apps {
		if reflect.DeepEqual(a.Source, spec) && !override {
			return fmt.Errorf("app with source: %v already exist. Pass --override to override", spec)
		}
	}
	app.Source = spec

	// Prepare a source seeker
	sourse, err := source.New(spec)
	if err != nil {
		return fmt.Errorf("failed to extract a source, error: %v", err)
	}
	var ss source.SourceSeeker
	var ok bool
	if ss, ok = sourse.(source.SourceSeeker); !ok {
		return fmt.Errorf("source doesn't support log seeking, source: %s", spec.Type)
	}
	if err := ss.Open(ctx); err != nil {
		return fmt.Errorf("failed to open the source, error: %v", err)
	}

	// Prepare the source to read logs
	logSize := int64(100)
	var logErr error
	foundLine := false
	for !foundLine && logSize > 1 {
		if _, logErr = ss.Seek(-logSize, io.SeekEnd); logErr != nil {
			return fmt.Errorf("failed to seek in the source, error: %v", logErr)
		}
		if _, logErr = ss.Next(ctx); logErr != nil {
			logSize = logSize / 2
			continue
		}
		foundLine = true
	}
	if !foundLine {
		return errors.New("failed to find logs in the source")
	}

	// Build the parser spec, store logs for further test
	var prev, curr parser.Spec
	var formatErr error
	lines := make([]string, 0, logSize)
	var line string
	for {
		line, formatErr = ss.Next(ctx)
		lines = append(lines, line)
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

	// Create the parser from the spec and test it on the logs
	p := parser.New(prev)
	for _, l := range lines {
		if _, err := p.Parse(l); err != nil {
			return fmt.Errorf("failed to test the created parser spec on logs, error: %v", err)
		}
	}

	parserId := generateName(slices.Collect(maps.Keys(cfg.Parsers)), prev.Type, name)

	// Check if parser spec already exist
	for id, s := range cfg.Parsers {
		if s.Type == prev.Type && reflect.DeepEqual(s.Params, prev.Params) {
			parserId = id
			break
		}
	}
	cfg.Parsers[parserId] = prev
	app.ParserID = parserId

	cfg.Apps = append(cfg.Apps, app)

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

var commonLogSynonyms = map[string][]string{
	"level_field": {
		"level", "lvl", "severity", "status", "priority", "loglevel",
		"sev", "s", "prio", "syslogpriority", "alertlevel",
	},
	"trace_id_field": {
		"traceid", "correlationid",
		"requestid", "reqid", "tid", "xtraceid",
		"oteltraceid", "spanid", "xrequestid",
		"xb3traceid", "operationid", "guid", "uid",
	},
	"message_field": {
		"message", "msg", "body", "text", "content", "logmsg", "description",
		"summary", "payload", "log", "eventmessage", "errormessage", "errmsg",
		"detail", "info", "raw", "data", "statement",
	},
	"timestamp_field": {
		"timestamp", "ts", "time", "recordedat", "createdat",
		"date", "logtime", "eventtime", "publishedat",
		"syslogtimestamp", "starttime", "epoch", "unixtimestamp", "millis",
		"asctime",
	},
}

func fillSpec(ps parser.Spec, data map[string]any) (parser.Spec, error) {
	params := make(map[string]string)
	for dk := range data {
		for gk, gv := range commonLogSynonyms {
			if _, ok := params[gk]; ok {
				continue
			}
			if slices.Contains(gv, prepareKey(dk)) {
				params[gk] = dk
			}
		}
	}
	var notFound []string

	for k := range commonLogSynonyms {
		if k == "trace_id_field" {
			continue
		}
		if _, ok := params[k]; !ok {
			notFound = append(notFound, k)
		}
	}
	if len(notFound) > 0 {
		return ps, fmt.Errorf("failed to find the following fields: %v", notFound)
	}

	dateStr := fmt.Sprint(data[params["timestamp_field"]])
	tsFormat := detectTimeFormat(dateStr)
	if tsFormat == "" {
		return ps, errors.New("failed to detect the timestamp format")
	}
	params["timestamp_format"] = tsFormat

	ps.Params = params
	return ps, nil
}

func prepareKey(s string) string {
	var b strings.Builder
	ls := strings.ToLower(s)
	for _, c := range ls {
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') {
			b.WriteRune(c)
		}
	}
	return b.String()
}

var commonTimeFormats = []string{
	"2006-01-02T15:04:05",
	"2006-01-02T15:04:05.999999999Z07:00",
	"2006-01-02T15:04:05.999999999",
	"2006-01-02T15:04:05.999999Z07:00",
	"2006-01-02T15:04:05.999999",
	"2006-01-02T15:04:05.999Z07:00",
	"2006-01-02T15:04:05.999",
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02 15:04:05",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05.999999",
	"2006-01-02 15:04:05.999",
	time.RFC1123,
	time.RFC1123Z,
	time.RFC822,
	time.RFC822Z,
	"2006/01/02 15:04:05",
	"01/02/2006 15:04:05",
	"02/01/2006 15:04:05",
	"2006-01-02",
	time.UnixDate,
	time.RubyDate,
}

func detectTimeFormat(timeStr string) string {
	if isNumeric(timeStr) {
		length := len(timeStr)
		switch {
		case length <= 10:
			return "unix"
		case length <= 13:
			return "unix_ms"
		case length <= 16:
			return "unix_us"
		default:
			return "unix_ns"
		}
	}

	for _, format := range commonTimeFormats {
		if _, err := time.Parse(format, timeStr); err == nil {
			return format
		}
	}
	return ""
}

func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func compareOrUpdate(prev, curr parser.Spec) (parser.Spec, error) {
	if prev.Type == "" {
		prev.Type = curr.Type
	} else if prev.Type != curr.Type {
		return prev, fmt.Errorf("spec types mismatch. Prev: %s, Curr: %s", prev.Type, curr.Type)
	}

	if prev.Params == nil {
		prev.Params = make(map[string]string)
	}

	for k, currV := range curr.Params {
		prevV, ok := prev.Params[k]
		if !ok {
			prev.Params[k] = currV
		} else if prevV != currV {
			return prev, fmt.Errorf("spec params mismatch, param: %s. Prev: %s, Curr: %s", k, prevV, currV)
		}
	}

	return prev, nil
}

func generateName(names []string, nameParts ...string) string {
	var num int
	name := _generateName(num, nameParts...)
outer:
	for {
		for _, n := range names {
			if n == name {
				num += 1
				name = _generateName(num, nameParts...)
				continue outer
			}
		}
		break
	}
	return name
}

func _generateName(num int, nameParts ...string) string {
	id := strings.Join(nameParts, "-")
	if num > 0 {
		id = id + "-" + strconv.Itoa(num)
	}
	return id
}
