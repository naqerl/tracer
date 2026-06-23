package llm

import (
	"net/http"
	"strings"

	"tracer/apps/panel/web/middleware"
)

func InitMux() *http.ServeMux {
	m := http.NewServeMux()

	// LLM documentation
	m.HandleFunc("GET /llm", llmDocs)

	return m
}

// llmDocs serves documentation about available markdown endpoints for LLM tools
func llmDocs(w http.ResponseWriter, r *http.Request) {
	// Render as markdown for /llm.md or regular HTML for /llm
	if middleware.IsMarkdownFormat(r) {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Write([]byte(markdownDocsContent(r.Host)))
	} else {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		llmDocsHTML(r.Host).Render(r.Context(), w)
	}
}

// markdownDocsContent returns the markdown content for LLM docs
func markdownDocsContent(baseURL string) string {
	var b strings.Builder

	b.WriteString("# Tracer Panel - LLM API Documentation\n\n")
	b.WriteString("Access tracer-panel logs and analytics programmatically using markdown-formatted endpoints.\n\n")
	b.WriteString("## Authentication\n\n")
	b.WriteString("All endpoints require authentication via API key.\n\n")
	b.WriteString("### Generate an API Key\n\n")
	b.WriteString("1. Navigate to Settings in the web UI\n")
	b.WriteString("2. Find the \"API Keys\" section\n")
	b.WriteString("3. Enter a descriptive name (e.g., \"Claude Desktop\")\n")
	b.WriteString("4. Click \"Generate Key\"\n")
	b.WriteString("5. Copy the generated key\n\n")
	b.WriteString("### Using Your API Key\n\n")
	b.WriteString("Use request header:\n")
	b.WriteString("```bash\n")
	b.WriteString("curl -H \"X-API-Key: YOUR_KEY\" \"https://" + baseURL + "/panel.md\"\n")
	b.WriteString("```\n\n")
	b.WriteString("## Permissions\n\n")
	b.WriteString("API keys inherit your user's app access permissions:\n")
	b.WriteString("- **Admins**: Access all apps and collectors\n")
	b.WriteString("- **Regular users**: Access only assigned apps\n\n")
	b.WriteString("## Available Endpoints\n\n")
	b.WriteString("### Analytics\n\n")
	b.WriteString("#### Dashboard\n")
	b.WriteString("```\n")
	b.WriteString("GET /panel.md\n")
	b.WriteString("```\n\n")
	b.WriteString("Lists all collectors with their status, system stats, and app counts.\n\n")
	b.WriteString("#### Collector Overview\n")
	b.WriteString("```\n")
	b.WriteString("GET /panel/collectors/{collector}.md\n")
	b.WriteString("```\n\n")
	b.WriteString("Shows system stats and applications for a specific collector.\n\n")
	b.WriteString("**Example:**\n")
	b.WriteString("```\n")
	b.WriteString("GET /panel/collectors/my-server.md\n")
	b.WriteString("```\n\n")
	b.WriteString("#### App Analytics\n")
	b.WriteString("```\n")
	b.WriteString("GET /panel/collectors/{collector}/apps/{app}.md\n")
	b.WriteString("```\n\n")
	b.WriteString("Shows statistics and grouped logs for a specific application (last 24 hours).\n\n")
	b.WriteString("**Example:**\n")
	b.WriteString("```\n")
	b.WriteString("GET /panel/collectors/my-server/apps/web-api.md\n")
	b.WriteString("```\n\n")
	b.WriteString("## Response Format\n\n")
	b.WriteString("All endpoints return markdown-formatted text with:\n")
	b.WriteString("- Headers for sections\n")
	b.WriteString("- Bullet lists for structured data\n")
	b.WriteString("- Code blocks for JSON context\n")
	b.WriteString("- Links to related resources\n\n")
	b.WriteString("## Common Use Cases\n\n")
	b.WriteString("### Check System Health\n")
	b.WriteString("```bash\n")
	b.WriteString("curl -H \"X-API-Key: YOUR_KEY\" \\\n")
	b.WriteString("  \"https://" + baseURL + "/panel.md\"\n")
	b.WriteString("```\n\n")
	b.WriteString("### View App Statistics and Logs\n")
	b.WriteString("```bash\n")
	b.WriteString("curl -H \"X-API-Key: YOUR_KEY\" \\\n")
	b.WriteString("  \"https://" + baseURL + "/panel/collectors/my-server/apps/web-api.md\"\n")
	b.WriteString("```\n\n")
	b.WriteString("### Monitor Specific Collector\n")
	b.WriteString("```bash\n")
	b.WriteString("curl -H \"X-API-Key: YOUR_KEY\" \\\n")
	b.WriteString("  \"https://" + baseURL + "/panel/collectors/my-server.md\"\n")
	b.WriteString("```\n\n")
	b.WriteString("## Rate Limits\n\n")
	b.WriteString("No rate limits are currently enforced, but please be respectful:\n")
	b.WriteString("- Cache responses when appropriate\n")
	b.WriteString("- Don't poll more frequently than once per minute\n")
	b.WriteString("- Use webhooks for real-time notifications (see notifiers in settings)\n\n")
	b.WriteString("## Security Notes\n\n")
	b.WriteString("- Keep your API keys secure and rotate them regularly\n")
	b.WriteString("- Each key is tied to your user account and inherits your permissions\n")
	b.WriteString("- You can delete keys at any time from the Settings page\n")
	b.WriteString("- Keys are displayed only once when generated\n\n")
	b.WriteString("## Need Help?\n\n")
	b.WriteString("Visit the web UI at `/llm` for an interactive version of this documentation.\n")

	return b.String()
}
