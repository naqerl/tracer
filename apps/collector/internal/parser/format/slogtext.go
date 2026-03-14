package format

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"tracer/libs/types"
)

// SlogText parses Go slog TextHandler formatted log lines.
// Format: time=RFC3339 level=LEVEL msg="message" key=value ...
// Groups are formatted as group.key=value.
// Values containing spaces, quotes, equals signs, or special chars are quoted with strconv.Quote.
type SlogText struct {
	Schema Schema
}

// token represents a parsed key-value pair
type token struct {
	key   string
	value string
}

// Parse parses a slog text format log line into a LogEntry.
// It handles:
//   - Standard fields: time, level, msg
//   - Source file/line when AddSource is enabled: source=/path/file.go:LINE
//   - Groups (key.prefixes): stored in Extra with full key name
//   - Quoted values with escaped characters (", \, \n, \t, etc.)
//   - Values with equals signs, spaces, special characters
func (p SlogText) Parse(line string) (types.LogEntry, error) {
	// Trim trailing whitespace but preserve internal formatting
	line = strings.TrimRightFunc(line, unicode.IsSpace)

	if line == "" {
		return types.LogEntry{}, fmt.Errorf("empty line")
	}

	tokens, err := tokenize(line)
	if err != nil {
		return types.LogEntry{}, fmt.Errorf("tokenize failed: %w", err)
	}

	if len(tokens) == 0 {
		return types.LogEntry{}, fmt.Errorf("no tokens found")
	}

	entry := types.LogEntry{
		Extra: make(map[string]string),
	}

	// First pass: identify and extract required fields
	var remaining []token
	for _, tok := range tokens {
		switch tok.key {
		case p.Schema.TimestampField:
			tsValue := unquoteValue(tok.value)
			parsedTS, err := parseTimestampString(tsValue, p.Schema.TimestampFormat)
			if err != nil {
				return types.LogEntry{}, fmt.Errorf("parse timestamp: %w", err)
			}
			entry.Timestamp = parsedTS
		case p.Schema.LevelField:
			entry.Level = normalizeLevel(unquoteValue(tok.value))
		case p.Schema.TraceIDField:
			entry.TraceID = unquoteValue(tok.value)
		case p.Schema.MessageField:
			entry.Message = unquoteValue(tok.value)
		default:
			remaining = append(remaining, tok)
		}
	}

	// Validate required timestamp
	if entry.Timestamp.IsZero() {
		return types.LogEntry{}, fmt.Errorf("missing timestamp field %q", p.Schema.TimestampField)
	}

	// If no message field found, use entire line as message
	if entry.Message == "" {
		entry.Message = line
	}

	// Store remaining fields in Extra
	for _, tok := range remaining {
		entry.Extra[tok.key] = unquoteValue(tok.value)
	}

	return entry, nil
}

// tokenize splits a slog text line into key=value tokens.
// Handles:
//   - Unquoted values (until next space): key=value
//   - Quoted values (handling escape sequences): key="value with \"quotes\""
//   - Empty quoted values: key=""
//   - Keys with dots for groups: outer.inner.key=value
func tokenize(line string) ([]token, error) {
	var tokens []token
	remaining := strings.TrimLeftFunc(line, unicode.IsSpace)

	for len(remaining) > 0 {
		// Find the '=' separating key from value
		eqIdx := strings.IndexByte(remaining, '=')
		if eqIdx <= 0 {
			// No more valid key=value pairs (key must be non-empty)
			break
		}

		key := remaining[:eqIdx]
		remaining = remaining[eqIdx+1:]

		if len(remaining) == 0 {
			// Value is empty (line ends with =)
			tokens = append(tokens, token{key: key, value: ""})
			break
		}

		// Parse the value
		value, rest := parseValue(remaining)
		tokens = append(tokens, token{key: key, value: value})
		remaining = strings.TrimLeftFunc(rest, unicode.IsSpace)
	}

	return tokens, nil
}

// parseValue extracts a value from the start of s.
// Returns the value (including quotes if present) and the remaining string.
func parseValue(s string) (string, string) {
	if len(s) == 0 {
		return "", ""
	}

	// Check if value is quoted
	if s[0] == '"' {
		return parseQuotedValue(s)
	}

	// Unquoted value - find end (next space or end of string)
	for i, r := range s {
		if unicode.IsSpace(r) {
			return s[:i], s[i:]
		}
	}
	return s, ""
}

// parseQuotedValue extracts a quoted string, handling escape sequences.
// The value includes the surrounding quotes.
// Returns the value and the remaining string after the value.
func parseQuotedValue(s string) (string, string) {
	if len(s) == 0 || s[0] != '"' {
		return "", s
	}

	// Find closing quote, handling escaped characters
	i := 1
	for i < len(s) {
		c := s[i]
		if c == '\\' && i+1 < len(s) {
			// Skip escaped character
			i += 2
		} else if c == '"' {
			// Found closing quote, include it
			i++
			break
		} else {
			i++
		}
	}

	value := s[:i]
	remaining := s[i:]
	return value, remaining
}

// unquoteValue removes surrounding quotes and unescapes Go string literals.
// For unquoted values, returns as-is.
// Returns empty string for empty quoted value "".
func unquoteValue(v string) string {
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		if unquoted, err := strconv.Unquote(v); err == nil {
			return unquoted
		}
		// If unquote fails, return without quotes but preserve content
		return v[1 : len(v)-1]
	}
	return v
}
