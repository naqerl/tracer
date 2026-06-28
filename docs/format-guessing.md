# Format Guessing

`tracer-collector add` analyzes log files or systemd journal entries to automatically detect log format and field mappings, then appends the app config to `/etc/tracer/collector.yaml`.

## Usage

```bash
# Add a systemd unit
tracer-collector add myapp

# Add a file path
tracer-collector add /var/log/myapp.log

# Skip confirmation prompt
tracer-collector add myapp --no-confirm
```

## How it works

1. **Detect source type** — input with `/` or `.` prefix is treated as a file path; otherwise a systemd unit
2. **Read samples** — reads the last 100 lines from the source
3. **Guess format** — detects JSON or slog text format
4. **Extract fields** — maps common field names (`level`, `msg`, `time`, etc.) to their actual names in your logs
5. **Preview and confirm** — shows detected format and sample parsed data, asks for confirmation
6. **Save** — writes the app to `collector.yaml`

## Supported formats

**JSON** with common field names:

| Concept | Recognized names |
|---|---|
| Level | `level`, `lvl`, `severity`, `priority`, `loglevel` |
| Message | `msg`, `message`, `body`, `text`, `content` |
| Timestamp | `ts`, `time`, `timestamp`, `@timestamp` |
| Trace ID | `trace_id`, `traceid`, `correlation_id`, `request_id` |

**Slog text** (Go structured logging):

```
time=2024-01-15T10:30:00Z level=INFO msg="Request completed" trace_id=abc123
```

## Resolving conflicts with `--favour`

When log lines use inconsistent field names, the collector reports a conflict. Fix them one at a time — each error message suggests the exact `--favour` value to use.

```
$ tracer-collector add myapp
...
Error: Could not analyze log format
Found inconsistent log format

First line (established format):
  {"ts":"2024-01-15T10:30:00Z","level":"INFO","msg":"started"}

Conflicting line:
  {"time":"2024-01-15T10:31:00Z","status":"ERROR","body":"failed"}

Issue: inconsistent level_field: found both 'level' and 'status'

To resolve, run again with:
  --favour 'level_field:"status"'
```

After resolving the last conflict, the detected configuration is shown and saved:

```
Detected log format:
  Parser: json
  Timestamp field: ts
  Timestamp format: 2006-01-02T15:04:05.999Z07:00
  Level field: level
  Message field: msg

Sample log line:
  {"ts":"2024-01-15T10:30:00Z","level":"INFO","msg":"..."}

Parsed fields from sample:
  level: INFO
  msg: started
  ts: 2024-01-15T10:30:00Z

Save this configuration to /etc/tracer/collector.yaml? [Y/n]:
```

### Favour syntax

```bash
# Single field
--favour 'level_field:"status"'

# Multiple fields
--favour 'level_field:"status",message_field:"body",timestamp_field:"time"'

# Force parser type (for mixed JSON/text)
--favour 'type:"json"'
```

The suggested `--favour` value uses the name from the *conflicting* line.

## Options

| Flag | Description |
|---|---|
| `--override` | Replace existing app config instead of erroring |
| `--favour` | Specify field mappings to resolve conflicts |
| `--no-confirm` | Skip confirmation prompt and save immediately |

## Requirements

- **Systemd units**: grant the collector user journal access — `sudo usermod -a -G systemd-journal tracer-collector`
- **File paths**: ensure the collector user has read access to the log file
