# Format Guessing

## Usage

```bash
# Add a systemd service
tracer-collector add myapp

# Add a file path
tracer-collector add /var/log/myapp.log

# Add with confirmation prompt (default)
tracer-collector add myapp

# Skip confirmation
tracer-collector add myapp --no-confirm
```

## Overview

The `tracer-collector add` command analyzes log files or systemd journal entries to automatically detect the log format and extract field mappings. It creates an app configuration that tells the collector how to parse logs from that source.

## How It Works

1. **Detect source type**: Automatically detects if the input is a file path (contains `/`) or a systemd unit
2. **Read sample logs**: Reads the last 100 lines from the source
3. **Guess format**: Detects if logs are JSON or slog text format
4. **Extract fields**: Maps common field names (e.g., `level`, `msg`, `time`) to the actual fields used in your logs
5. **Preview and confirm**: Shows detected format and sample parsed data, asks for confirmation
6. **Save configuration**: Adds the app to collector.yaml

## Supported Formats

### JSON
Detects JSON logs with common field names:
- **Level**: `level`, `lvl`, `severity`, `priority`, `loglevel`
- **Message**: `msg`, `message`, `body`, `text`, `content`
- **Timestamp**: `ts`, `time`, `timestamp`, `@timestamp`
- **Trace ID**: `trace_id`, `traceid`, `correlation_id`, `request_id`

### Slog Text
Detects Go slog text format:
```
time=2024-01-15T10:30:00Z level=INFO msg="Request completed" trace_id=abc123
```

## Resolving Conflicts with --favour

When log files have inconsistent formats (different field names across lines), the collector will report a conflict. You resolve these iteratively using the `--favour` flag.

### Example Workflow

**Step 1: Try to add the app**
```bash
$ tracer-collector add myapp
Adding log source: myapp
  Type: systemd unit
  Unit: myapp

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

**Step 2: Resolve the first conflict**
```bash
$ tracer-collector add myapp --favour 'level_field:"status"'
Adding log source: myapp
  Type: systemd unit
  Unit: myapp

Error: Could not analyze log format

Found inconsistent log format

First line (established format):
  {"ts":"2024-01-15T10:30:00Z","level":"INFO","msg":"started"}

Conflicting line:
  {"time":"2024-01-15T10:31:00Z","status":"ERROR","body":"failed"}

Issue: inconsistent message_field: found both 'msg' and 'body'

To resolve, run again with:
  --favour 'message_field:"body"'
```

**Step 3: Resolve the next conflict**
```bash
$ tracer-collector add myapp --favour 'message_field:"body"'
# If more conflicts exist, repeat the process
# Eventually:

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

### Favour Syntax

The `--favour` flag accepts a comma-separated list of field mappings:

```bash
# Single field
--favour 'level_field:"status"'

# Multiple fields
--favour 'level_field:"status",message_field:"body",timestamp_field:"time"'

# Specify parser type (for mixed JSON/text logs)
--favour 'type:"json"'
```

### Common Conflict Scenarios

1. **Mixed field names**: Some lines use `level`, others use `status` or `severity`
2. **Different timestamp fields**: Some lines use `ts`, others use `time` or `@timestamp`
3. **Different message fields**: Some lines use `msg`, others use `message` or `body`
4. **Mixed format types**: Some lines are JSON, others are slog text

### Tips

- Fix conflicts one at a time - the error message always tells you the next issue
- The suggested `--favour` value uses the value from the *conflicting* line
- Once you resolve all conflicts, the configuration is saved
- Use `--no-confirm` to skip the final Y/n prompt

## Available Options

| Flag | Description |
|------|-------------|
| `--override` | Replace existing app configuration |
| `--favour` | Specify field mappings to resolve conflicts |
| `--no-confirm` | Skip confirmation prompt and save immediately |

## Requirements

For systemd units, ensure the collector user has permission to read the journal:
```bash
sudo usermod -a -G systemd-journal tracer-collector
```

For file paths, ensure the collector user has read access to the log file.
