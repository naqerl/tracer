package formatguesser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"tracer/apps/collector/internal/parser"
	"tracer/apps/collector/internal/parser/format"
	"tracer/apps/collector/internal/source"
	"tracer/libs/types"
)

type AddRequest struct {
	Override bool
	Favour   parser.Spec // Manual parser spec to resolve conflicts
}

// AddResult contains the result of adding an app including preview data
type AddResult struct {
	App         types.AppSpec
	ParserSpec  parser.Spec
	SampleLines []string
	SampleData  map[string]string
}

// ErrNoLogs reports that the source exists but produced no log lines, so
// nothing could be guessed from it.
var ErrNoLogs = errors.New("no logs found in the source")

func Add(ctx context.Context, cfg *types.CollectorConfig, name string, spec source.Spec, req AddRequest) (*AddResult, error) {
	result := &AddResult{}
	var app types.AppSpec
	existingIdx := -1

	appNames := make([]string, 0, len(cfg.Apps))
	for i, v := range cfg.Apps {
		appNames = append(appNames, v.Name)
		if v.Source != nil && reflect.DeepEqual(*v.Source, types.SourceSpec{Type: spec.Type, Params: spec.Params}) {
			if !req.Override {
				return nil, fmt.Errorf("app with this source already exists. Pass --override to replace it")
			}
			existingIdx = i
			app.Name = v.Name
		}
	}
	if existingIdx == -1 {
		app.Name = generateName(appNames, name)
	}
	app.Source = &types.SourceSpec{Type: spec.Type, Params: spec.Params}

	src, err := source.New(spec)
	if err != nil {
		return nil, fmt.Errorf("failed to create source reader: %w", err)
	}
	var ss source.SourceSeeker
	var ok bool
	if ss, ok = src.(source.SourceSeeker); !ok {
		return nil, fmt.Errorf("source type '%s' does not support reading", spec.Type)
	}
	if err := ss.Open(ctx); err != nil {
		return nil, fmt.Errorf("failed to open log source: %w", err)
	}
	defer func() {
		_ = ss.Close()
	}()

	buildResult, err := buildParserSpec(ctx, ss, req.Favour)
	if err != nil {
		return nil, err
	}

	p := parser.New(buildResult.Spec)
	for _, l := range buildResult.Lines {
		if _, err := p.Parse(l); err != nil {
			return nil, fmt.Errorf("parser test failed on sample line: %w", err)
		}
	}

	app.Parser = &types.ParserSpec{Type: buildResult.Spec.Type, Params: buildResult.Spec.Params}

	if existingIdx >= 0 {
		cfg.Apps[existingIdx] = app
	} else {
		cfg.Apps = append(cfg.Apps, app)
	}

	result.App = app
	result.ParserSpec = buildResult.Spec
	result.SampleLines = buildResult.Lines
	result.SampleData = buildResult.Parsed
	return result, nil
}

// BuildResult holds the result of building a parser spec including sample lines
type BuildResult struct {
	Spec   parser.Spec
	Lines  []string
	Parsed map[string]string // sample parsed fields from first line
}

// maxSampleLines caps how many log lines the guesser reads from a source.
const maxSampleLines = 100

func buildParserSpec(ctx context.Context, ss source.SourceSeeker, favour parser.Spec) (BuildResult, error) {
	result := BuildResult{
		Parsed: make(map[string]string),
	}

	// Seek toward the end of the log. The first line read from a byte-addressed
	// file source may be a partial tail, so it is primed and discarded; the
	// lines that follow are complete.
	//
	// Only an exhausted source justifies shrinking the window and retrying. Any
	// other error means the source could not be read at all, and retrying hides
	// the reason behind a bare "no logs found".
	logSize := 100
	foundLine := false
	var readErr error
	for !foundLine && logSize > 1 {
		if _, logErr := ss.Seek(int64(-logSize), io.SeekEnd); logErr != nil {
			readErr = logErr
			logSize = logSize / 2
			continue
		}
		if _, logErr := ss.Next(ctx); logErr != nil {
			if !errors.Is(logErr, io.EOF) {
				return result, fmt.Errorf("could not read from source: %w", logErr)
			}
			readErr = logErr
			logSize = logSize / 2
			continue
		}
		foundLine = true
	}

	// Files shorter than the smallest seek window (e.g. a 2-byte file) can
	// never satisfy a negative end seek; read from the start instead.
	if !foundLine {
		if _, err := ss.Seek(0, io.SeekStart); err != nil {
			return result, fmt.Errorf("could not seek in the source: %w", err)
		}
		if _, err := ss.Next(ctx); err != nil {
			if readErr != nil && !errors.Is(err, readErr) {
				return result, fmt.Errorf("could not read from source: %w", err)
			}
			return result, ErrNoLogs
		}
		foundLine = true
	}
	if !foundLine {
		return result, ErrNoLogs
	}

	var prev parser.Spec
	var lines []string
	var firstLine string
	var firstParsed map[string]any

	// collect reads and processes one complete line. It returns false when the
	// source is exhausted (io.EOF) and true when a line was collected.
	collect := func() (bool, error) {
		line, err := ss.Next(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return false, nil
			}
			return false, fmt.Errorf("could not read from source: %w", err)
		}

		curr, parsed, err := guessParserSpecWithData(line, favour)
		if err != nil {
			return false, GuessError{Line: line, Err: err}
		}

		if firstLine == "" {
			firstLine = line
			firstParsed = parsed
		}

		next, conflict, err := compareOrUpdate(prev, curr, favour)
		if err != nil {
			return false, ConsistencyError{
				Line:      line,
				FirstLine: firstLine,
				Err:       err,
				Field:     conflict.Field,
				PrevValue: conflict.PrevValue,
				CurrValue: conflict.CurrValue,
				PrevSpec:  prev,
				CurrSpec:  curr,
			}
		}
		prev = next
		lines = append(lines, line)
		return true, nil
	}

	for len(lines) < maxSampleLines {
		ok, err := collect()
		if err != nil {
			return result, err
		}
		if !ok {
			break
		}
	}

	if len(lines) == 0 {
		// The tail seek landed past every complete line (e.g. the last line is
		// longer than the seek window). Fall back to the start of the log.
		if _, err := ss.Seek(0, io.SeekStart); err != nil {
			return result, fmt.Errorf("could not seek in the source: %w", err)
		}
		for len(lines) < maxSampleLines {
			ok, err := collect()
			if err != nil {
				return result, err
			}
			if !ok {
				break
			}
		}
	}

	if len(lines) == 0 {
		return result, errors.New("could not read any complete log lines from the source")
	}

	// Populate parsed fields from first line for preview
	for k, v := range firstParsed {
		result.Parsed[k] = fmt.Sprint(v)
	}

	result.Spec = prev
	result.Lines = lines
	return result, nil
}

// GuessError represents an error when guessing parser format
type GuessError struct {
	Line string
	Err  error
}

func (e GuessError) Error() string {
	return fmt.Sprintf("failed to parse log line: %v", e.Err)
}

func (e GuessError) Unwrap() error {
	return e.Err
}

// MissingFieldsError reports which fields could not be found in a log line.
// Fields holds the missing required parser field keys ("level_field",
// "message_field", "timestamp_field"); Optional holds missing optional fields
// ("trace_id_field"), whose absence callers may present as ignorable. Keys
// lists every field name present in the offending line (sorted), so callers
// can offer the user a choice of which key maps to each missing field.
type MissingFieldsError struct {
	Fields   []string
	Optional []string
	Keys     []string
}

func (e *MissingFieldsError) Error() string {
	return fmt.Sprintf("failed to find the following fields: %v", e.Fields)
}

// ConsistencyError represents an error when log lines have inconsistent formats
type ConsistencyError struct {
	Line      string      // The problematic line
	FirstLine string      // The first line we saw (for comparison)
	Err       error       // The specific error
	Field     string      // The specific conflicting field (e.g., "level_field", "type")
	PrevValue string      // Previous value
	CurrValue string      // Current (conflicting) value
	PrevSpec  parser.Spec // Full previous spec
	CurrSpec  parser.Spec // Full current spec
}

func (e ConsistencyError) Error() string {
	return fmt.Sprintf("log format inconsistent: %v", e.Err)
}

func (e ConsistencyError) Unwrap() error {
	return e.Err
}

// SuggestFavour returns a minimal --favour flag value to resolve just this conflict
func (e ConsistencyError) SuggestFavour() string {
	if e.Field == "type" {
		return fmt.Sprintf(`--favour 'type:"%s"'`, e.CurrValue)
	}
	return fmt.Sprintf(`--favour '%s:"%s"'`, e.Field, e.CurrValue)
}

func guessParserSpec(line string) (parser.Spec, error) {
	ps, _, err := guessParserSpecWithData(line, parser.Spec{})
	return ps, err
}

func guessParserSpecWithData(line string, favour parser.Spec) (parser.Spec, map[string]any, error) {
	var ps parser.Spec

	var jsonData map[string]any
	err := json.Unmarshal([]byte(line), &jsonData)
	if err == nil {
		ps.Type = "json"
		result, err := fillSpec(ps, jsonData, favour)
		return result, jsonData, err
	}
	slog.Debug("failed to unmarshal the line to JSON")

	tempSlogText, err := format.UnmarshalSlogText(line)
	if err == nil {
		ps.Type = "slogtext"
		slogTextData := make(map[string]any, len(tempSlogText))
		for k, v := range tempSlogText {
			slogTextData[k] = v
		}
		result, err := fillSpec(ps, slogTextData, favour)
		return result, slogTextData, err
	}
	slog.Debug("failed to unmarshal the line to SlogText")

	return ps, nil, errors.New("log format not recognized. Expected JSON or slog text format")
}

var commonLogSynonyms = map[string][]string{
	"level_field": {
		"level", "lvl", "severity", "status", "priority", "loglevel",
		"sev", "s", "prio", "syslogpriority", "alertlevel", "levelname",
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

// knownLogLevels contains common log level values to help us identify the actual level field
var knownLogLevels = map[string]bool{
	"debug": true, "info": true, "warn": true, "warning": true,
	"error": true, "fatal": true, "panic": true, "trace": true,
	"i": true, "w": true, "e": true, "d": true, // short forms
}

// isLogLevel checks if a value looks like a log level (not a numeric HTTP status)
func isLogLevel(v any) bool {
	s := strings.ToLower(fmt.Sprint(v))
	return knownLogLevels[s]
}

// fieldPriority defines which field names are preferred when multiple matches exist
var fieldPriority = map[string]int{
	"level": 1, "lvl": 2, "severity": 3, "status": 10, // prefer "level" over "status"
	"ts": 1, "timestamp": 2, "time": 3,
	"msg": 1, "message": 2, "body": 3,
}

func fillSpec(ps parser.Spec, data map[string]any, favour parser.Spec) (parser.Spec, error) {
	// First pass: collect all potential matches for each field
	potentialMatches := make(map[string][]string) // field -> []dataKey
	for dk := range data {
		for gk, gv := range commonLogSynonyms {
			if slices.Contains(gv, prepareKey(dk)) {
				potentialMatches[gk] = append(potentialMatches[gk], dk)
			}
		}
	}

	params := make(map[string]string)

	// For each field, pick the best match
	for gk, matches := range potentialMatches {
		if len(matches) == 0 {
			continue
		}

		// An explicit --favour mapping wins over synonym guessing
		if favoured := favouredKey(favour, gk, data); favoured != "" {
			params[gk] = favoured
			continue
		}

		// For level_field, prefer the one with a valid log level value
		if gk == "level_field" && len(matches) > 1 {
			bestMatch := ""
			bestPriority := 999
			for _, dk := range matches {
				priority := fieldPriority[prepareKey(dk)]
				if priority == 0 {
					priority = 999
				}
				// If this field has a valid log level value, boost its priority
				if isLogLevel(data[dk]) && priority < bestPriority {
					bestPriority = priority
					bestMatch = dk
				}
			}
			if bestMatch != "" {
				params[gk] = bestMatch
				continue
			}
		}

		// Default: pick the one with lowest priority number (or first in list)
		bestMatch := matches[0]
		bestPriority := fieldPriority[prepareKey(bestMatch)]
		if bestPriority == 0 {
			bestPriority = 999
		}
		for _, dk := range matches[1:] {
			priority := fieldPriority[prepareKey(dk)]
			if priority == 0 {
				priority = 999
			}
			if priority < bestPriority {
				bestPriority = priority
				bestMatch = dk
			}
		}
		params[gk] = bestMatch
	}

	// Fall back to --favour for fields that no synonym matched. This covers
	// optional fields too (trace_id_field): the favour value names a key that
	// must actually exist in the line, and optionalNotFound is computed below
	// so a successful mapping stays out of the missing list.
	for k := range commonLogSynonyms {
		if _, ok := params[k]; ok {
			continue
		}
		if favoured := favouredKey(favour, k, data); favoured != "" {
			params[k] = favoured
		}
	}

	var notFound []string
	var optionalNotFound []string
	for k := range commonLogSynonyms {
		if k == "trace_id_field" {
			if _, ok := params[k]; !ok {
				optionalNotFound = append(optionalNotFound, k)
			}
			continue
		}
		if _, ok := params[k]; !ok {
			notFound = append(notFound, k)
		}
	}
	if len(notFound) > 0 {
		keys := make([]string, 0, len(data))
		for dk := range data {
			keys = append(keys, dk)
		}
		slices.Sort(keys)
		return ps, &MissingFieldsError{Fields: notFound, Optional: optionalNotFound, Keys: keys}
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

// favouredKey returns the --favour mapping for a parser field (e.g.
// "timestamp_field") when it names a key that exists in the line, otherwise
// an empty string.
func favouredKey(favour parser.Spec, field string, data map[string]any) string {
	if favour.Params == nil {
		return ""
	}
	key, ok := favour.Params[field]
	if !ok || key == "" {
		return ""
	}
	if _, exists := data[key]; !exists {
		return ""
	}
	return key
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

// Conflict holds details about a format conflict
type Conflict struct {
	Field     string
	PrevValue string
	CurrValue string
}

func compareOrUpdate(prev, curr, favour parser.Spec) (parser.Spec, Conflict, error) {
	emptyConflict := Conflict{}

	if prev.Type == "" {
		prev.Type = curr.Type
	} else if prev.Type != curr.Type {
		// Type mismatch - use favour if provided
		if favour.Type != "" {
			// Keep the current spec but override the type
			prev.Type = favour.Type
			return prev, emptyConflict, nil
		}
		return prev, Conflict{Field: "type", PrevValue: prev.Type, CurrValue: curr.Type},
			fmt.Errorf("mixed log format types: %s vs %s", prev.Type, curr.Type)
	}

	if prev.Params == nil {
		prev.Params = make(map[string]string)
	}

	for k, currV := range curr.Params {
		prevV, ok := prev.Params[k]
		if !ok {
			prev.Params[k] = currV
		} else if prevV != currV {
			// Use favour if it has a value for this field
			if favour.Params != nil {
				if favouredV, ok := favour.Params[k]; ok && favouredV != "" {
					prev.Params[k] = favouredV
					continue
				}
			}
			return prev, Conflict{Field: k, PrevValue: prevV, CurrValue: currV},
				fmt.Errorf("inconsistent %s: found both '%s' and '%s'", k, prevV, currV)
		}
	}

	return prev, emptyConflict, nil
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
