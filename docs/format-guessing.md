# Format guessing

## Usage
``` bash
tracer-collector add --src [journalctl] <unit>
```

## Overview

Looks inside of the logs source for specified unit, guesses the log format and extracts the specific parameters.
Adds an 'app' record inside of the collector configuration file.

## Available options

Source:
- `journalctl`

Log format:
- `JSON`

## Requirements

`journalctl`
Make sure that current user have the permission to run the `journalctl`.
The unit name have to match the name of the service ("unit name".service).

## Workflow

1. Inside of main read the config and check if the app present there. Override only with --override flag, if no flag exit with warning.
2. Validate the input.
3. Read the last 100 logs from the source.
4. Guess the log format by detecting the format specific characters and patterns. Exit the execution if the format was not detected.
5. Parse the logs and extract the required parameters:
    - Log level
    - Trace id
    - Message
    - Timestamp (format as well)
    Match the keys with the pool of most used. (Example: "msg", "message", "descr", "description" for the Message parameter).
    Exit if required parameters: log level and message are not found. If logs are inconsistent log WARN and pick first among options.
6. Add a new app to the collector configuration file. If app entry with such name already exists, overwrite the fields.

## Implementation

```go
package formatguesser

type LogSorce interface {
    Fetch(ctx context.Context) ([]byte, error)
}

func Add(ctx context.Context, cfg config.Collector, sources config.Sources)

func guessFormat(raw []byte) (config.ResolvedApp, error)

```

## Test cases

Create a table test with:
    Input: Log in any format
    Expected: types.LogEntry
    Execution: log -> guessFormat -> config.ReslvedApp -> parser.Parse(ra) -> compare with expected

## Notes:
1. Add the Persist method to the Collector type in /collector/internal/config
