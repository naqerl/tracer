package panel

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseFavour(t *testing.T) {
	field := parseFavour("level_field", "status")
	if field.Params == nil || field.Params["level_field"] != "status" {
		t.Fatalf("field favour = %+v, want Params[level_field]=status", field)
	}

	typ := parseFavour("type", "slogtext")
	if typ.Type != "slogtext" {
		t.Fatalf("type favour = %+v, want Type=slogtext", typ)
	}

	empty := parseFavour("", "")
	if empty.Type != "" || len(empty.Params) != 0 {
		t.Fatalf("empty favour = %+v, want zero spec", empty)
	}
}

func TestParseParserSpecLineKeys(t *testing.T) {
	spec, err := parseParserSpec("json", "level_field: status\nmessage_field: msg\ntimestamp_field: time\ntimestamp_format: 2006-01-02T15:04:05")
	if err != nil {
		t.Fatalf("parseParserSpec: %v", err)
	}
	if spec.Type != "json" {
		t.Fatalf("type = %q, want json", spec.Type)
	}
	want := map[string]string{
		"level_field":      "status",
		"message_field":    "msg",
		"timestamp_field":  "time",
		"timestamp_format": "2006-01-02T15:04:05",
	}
	got, err := json.Marshal(spec.Params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	exp, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal want: %v", err)
	}
	if !bytes.Equal(got, exp) {
		t.Fatalf("params = %s, want %s", got, exp)
	}
}

func TestParseParserSpecJSON(t *testing.T) {
	spec, err := parseParserSpec("slogtext", `{"level_field":"status","message_field":"msg","timestamp_field":"time","timestamp_format":"2006-01-02T15:04:05"}`)
	if err != nil {
		t.Fatalf("parseParserSpec: %v", err)
	}
	if spec.Type != "slogtext" {
		t.Fatalf("type = %q, want slogtext", spec.Type)
	}
	if spec.Params["level_field"] != "status" {
		t.Fatalf("level_field = %q, want status", spec.Params["level_field"])
	}
}

func TestParseParserSpecBlank(t *testing.T) {
	spec, err := parseParserSpec("json", "")
	if err != nil {
		t.Fatalf("parseParserSpec: %v", err)
	}
	if spec.Type != "json" || len(spec.Params) != 0 {
		t.Fatalf("spec = %+v, want json with empty params", spec)
	}
}

func TestParserParamsFromForm(t *testing.T) {
	form := "parser_type=json&parser_param_level_field=level&parser_param_message_field=msg&parser_param_trace_id_field=&unrelated=value"
	r := httptest.NewRequest("POST", "/", strings.NewReader(form))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	params := parserParamsFromForm(r)
	want := map[string]string{
		"level_field":   "level",
		"message_field": "msg",
	}
	if got, err := json.Marshal(params); err != nil {
		t.Fatalf("marshal params: %v", err)
	} else if wantJSON, _ := json.Marshal(want); !bytes.Equal(got, wantJSON) {
		t.Fatalf("params = %s, want %s", got, wantJSON)
	}
}
