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

The path to the config have to be mentioned in the .env or passed via flag, otherwise the default path will be used.
Priority: Flag > Environmental variable > Default Path

`journalctl`
Make sure that current user have the permission to run the `journalctl`.
The unit name have to match the name of the service ("unit name".service).

## Workflow

1. Validate the input.
2. Check if source is available for current unit.
3. <TBD> Read the last 100 logs from the source.
4. Guess the log format by detecting the format specific characters and patterns. Exit the execution if the format was not detected.
5. Parse the logs and extract the required parameters:
    - Log level
    - Trace id
    - Message
    Match the keys with the pool of most used. (Example: "msg", "message", "descr", "description" for the Message parameter).
    <TBD> Exit if required parameters: log level and message, are not found or inconsistent among these logs.
6. Add a new app to the collector configuration file. <TBD> If app entry with such name already exists, overwrite the fields.

## Implementation

```go
package formatguesser

type LogsFormat int

const (
    JSON LogsFormat = iota
)

type Params struct {
    LogLevel    string
    Message     string
    TraceID     string
}

type Parser interface {
    Parse(raw []byte) (Params, error)
}

type LogSorce interface {
    Fetch(unit string) ([]byte, error)
}

func Add(source, unit, configPath string)

func guessFormat(raw []byte) (Parser, error)

func updateConfig(configPath string, params Params) (error)
```

## Test cases

### Positive

1. Standard systemd JSON
    Input: 1 to 100 lines of valid JSON format from journalctl
    Expectations: `guessFormat` returns JSON parser, which populates all the Params successfully. Config file gets updated.

### Negative

1. Unavailable source
    Input: Arbitrary source
    Expectations: `Add` exits with error before fetching the source

2. No permissions
    Input: User has no permission to run `journalctl`
    Expectations: `Fetch` will return an error and `Add` exits

3. Unavailable format
    Input: Plain text logs
    Expectations: `guessFormat` returns an error and `Add` exits

4. Wrong guess
    Input: `guessFormat` makes a wrong guess
    Expectations: `Parse`  returns an error and `Add` exits
