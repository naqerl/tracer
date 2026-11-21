package mcp

import (
	"encoding/json"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"tracer/libs/types"
)

func TestCreateCommandHealthApp(t *testing.T) {
	e := newEndpoint(t, false)
	session := e.connect(t, e.userKey(t, e.admin.ID, "mcp"))

	res, err := session.CallTool(t.Context(), &mcpsdk.CallToolParams{
		Name: "create_app",
		Arguments: map[string]any{
			"collector":     "demo",
			"name":          "pg-mcp",
			"health_type":   "command",
			"command":       "pg_isready",
			"expect":        "accepting connections",
			"expected_exit": "0,2",
			"interval":      "60s",
		},
	})
	if err != nil {
		t.Fatalf("call create_app: %v", err)
	}
	if res.IsError {
		t.Fatalf("create_app reported an error: %s", contentText(t, res))
	}

	raw, err := e.panelSvc.FetchCollectorConfig(t.Context(), "demo")
	if err != nil {
		t.Fatalf("fetch config: %v", err)
	}
	var cfg types.CollectorConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	app, ok := cfg.GetApp("pg-mcp")
	if !ok || app.Health == nil {
		t.Fatalf("app = %+v, want a command health check", app)
	}
	if app.Health.Type != "command" || app.Health.Params["command"] != "pg_isready" ||
		app.Health.Params["expect"] != "accepting connections" ||
		app.Health.Params["expected_exit"] != "0,2" {
		t.Fatalf("health = %+v", app.Health)
	}
}
