package settings

import (
	"fmt"
	"net/url"
	"strings"
)

// shellQuote wraps a value in single quotes so a shell treats it literally.
// Single quotes suspend every expansion, including the ones inside the value.
// An embedded single quote is closed, escaped, and reopened.
func shellQuote(value string) string {
	return `'` + strings.ReplaceAll(value, `'`, `'\''`) + `'`
}

// buildCollectorInstallCommand returns the one-line command a user pastes on a
// collector host. The fetched script installs the collector when missing and
// upgrades it otherwise, so the same command serves both a fresh host and an
// existing one. The URL is shell-quoted as a whole and its collector segment is
// percent-encoded, so a name with shell metacharacters stays one literal
// argument and still resolves to the right collector.
func buildCollectorInstallCommand(host, collectorName, apiKey string) string {
	installURL := fmt.Sprintf(
		"%s/panel/collectors/%s/install-script",
		strings.TrimRight(strings.TrimSpace(host), "/"),
		url.PathEscape(strings.TrimSpace(collectorName)),
	)
	return fmt.Sprintf(
		`curl -fsSL %s -H %s | sudo bash`,
		shellQuote(installURL),
		shellQuote("X-API-Key: "+apiKey),
	)
}
