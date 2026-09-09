# Format Guessing

When you add an app in the panel, the collector auto-detects the log format and field mappings for a named service (systemd unit, kubectl resource, etc.). The collector runs the guesser and returns the detected source and parser to the panel over WebSocket.

## How the flow works

1. **User inputs a service name** — the add-app form in the panel asks only for a name (optional health-check URL for uptime-only monitoring). The service name becomes the app name.
2. **Panel sends the request to the collector** — a format-request event goes over the live WebSocket connection.
3. **Collector runs the format guesser** — it checks the systemd journal default action, and falls back to kubectl if the service is a resource reference.
4. **Panel receives the result** — the detected source and parser config come back over the same WebSocket and the app is saved and pushed to the collector.
5. **Collector hot-reloads** — the supervisor reconciles and starts the new app without a restart.

The collector runs entirely panel-driven: it has no local app configuration. On connect it registers with an empty app list and the panel pushes the stored config over the WebSocket.

## Communication is via WebSocket

Format guessing is round-tripped over the live WebSocket, not separate HTTP. The panel forwards the request, the collector replies with the detected source/parser, and the panel can then build and push the full app config back.

## Detected formats

The guesser recognizes **JSON** and **slog text** (Go structured logging) with common field synonyms:

| Concept | Recognized names |
|---|---|
| Level | `level`, `lvl`, `severity`, `priority`, `loglevel` |
| Message | `msg`, `message`, `body`, `text`, `content` |
| Timestamp | `ts`, `time`, `timestamp`, `@timestamp` |
| Trace ID | `trace_id`, `traceid`, `correlation_id`, `request_id` |

Slog text example:

```
time=2024-01-15T10:30:00Z level=INFO msg="Request completed" trace_id=abc123
```

## Requirements

- **Systemd units**: grant the collector user journal access — `sudo usermod -a -G systemd-journal tracer-collector`
- **File paths**: ensure the collector user has read access to the log file
- **Kubectl sources**: configure a kubeconfig the collector can use (see `sudo tracer-collector kubectl-setup`)
