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

When a log line is missing a required field entirely, the collector reports which fields are missing and lists every key present in the offending line:

```
$ tracer-collector add deployment/tracer
...
Error: Could not analyze log format

Could not parse this log line:
  when=2026-08-02T09:58:45.457Z level=INFO msg="HTTP Request" method=GET path=/auth/login

Reason: missing required field(s): timestamp

Fields with level, message, timestamp are required

Optional field(s) not found: trace_id (may be ignored)

Available keys in the log line:
  duration_ms, level, method, msg, path, when
```

On a terminal it then offers an interactive mapping for each missing field — you pick which key from the line to use, and it retries automatically. Missing required fields must each be mapped (press Enter to give up); missing optional fields such as `trace_id` can be ignored by pressing Enter:

```
Which field should be used for timestamp?
  1) duration_ms
  2) level
  3) method
  4) msg
  5) path
  6) when
Select a number (or press Enter to skip): 6
Which field should be used for trace_id?
  1) duration_ms
  2) level
  3) method
  4) msg
  5) path
  6) when
Select a number (or press Enter to ignore):
Retrying with --favour 'timestamp_field:"when"'...
```

The retry maps the chosen fields and continues to the preview; ignored optional fields are simply left unmapped.

`--favour` also works when a required field uses a key the guesser does not recognize — the mapped key just has to exist in the log line.

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
| `--favour` | Specify field mappings to resolve conflicts or fill in missing fields |
| `--no-confirm` | Skip confirmation prompt and save immediately |

Flags may appear before or after the source argument, e.g. both
`tracer-collector add --favour 'level_field:"status"' myapp` and
`tracer-collector add myapp --no-confirm` work.

## Requirements

- **Systemd units**: grant the collector user journal access — `sudo usermod -a -G systemd-journal tracer-collector`
- **File paths**: ensure the collector user has read access to the log file
