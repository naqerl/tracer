package health

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"tracer/libs/types"
)

// CommandChecker runs a shell command and reports up only when it exits with
// an expected code and its combined output matches the expected pattern.
type CommandChecker struct {
	command      string
	expect       *regexp.Regexp
	timeout      time.Duration
	expectedExit map[int]struct{}
}

type CommandConfig struct {
	Command      string
	Expect       *regexp.Regexp
	Timeout      time.Duration
	ExpectedExit map[int]struct{}
}

func (CommandConfig) Default() CommandConfig {
	return CommandConfig{
		Timeout:      5 * time.Second,
		ExpectedExit: map[int]struct{}{0: {}},
	}
}

func NewCommandChecker(cfg CommandConfig) *CommandChecker {
	return &CommandChecker{
		command:      cfg.Command,
		expect:       cfg.Expect,
		timeout:      cfg.Timeout,
		expectedExit: cfg.ExpectedExit,
	}
}

func (c *CommandChecker) Check(ctx context.Context) Result {
	startedAt := time.Now().UTC()
	output, exitCode, err := c.run(ctx)
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && c.exitExpected(exitCode) {
		err = nil
	}
	if err == nil {
		if !c.exitExpected(exitCode) {
			err = fmt.Errorf("unexpected exit code %d", exitCode)
		} else if c.expect != nil && !c.expect.Match(output) {
			err = fmt.Errorf("output did not match %q", c.expect.String())
		}
	}
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("health check failed",
				"command", c.command,
				"exit_code", exitCode,
				"error", err,
				"output", outputTail(output),
			)
		}
		return Result{Time: startedAt, Status: types.HealthStatusDown}
	}
	return Result{Time: startedAt, Status: types.HealthStatusUp}
}

// run executes the command in its own process group so a timeout can kill the
// shell and every child it spawned.
func (c *CommandChecker) run(ctx context.Context) ([]byte, int, error) {
	runCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var output bytes.Buffer
	cmd := exec.CommandContext(runCtx, "/bin/sh", "-c", c.command)
	cmd.Stdout = &output
	cmd.Stderr = &output
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	cmd.WaitDelay = time.Second

	err := cmd.Run()
	exitCode := -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}

	if errors.Is(runCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
		return output.Bytes(), exitCode, fmt.Errorf("timed out after %s", c.timeout)
	}
	if ctx.Err() != nil {
		return output.Bytes(), exitCode, ctx.Err()
	}
	if err != nil {
		return output.Bytes(), exitCode, err
	}
	return output.Bytes(), exitCode, nil
}

func (c *CommandChecker) exitExpected(code int) bool {
	_, ok := c.expectedExit[code]
	return ok
}

func specCommandConfig(s types.HealthCheckSpec) (CommandConfig, error) {
	command, err := getParam(s, "command")
	if err != nil {
		return CommandConfig{}, err
	}

	cfg := CommandConfig{}.Default()

	if raw := strings.TrimSpace(s.Params["timeout"]); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return CommandConfig{}, fmt.Errorf("invalid params.timeout: %w", err)
		}
		if d <= 0 {
			return CommandConfig{}, fmt.Errorf("invalid params.timeout: must be positive")
		}
		cfg.Timeout = d
	}

	if raw := strings.TrimSpace(s.Params["expected_exit"]); raw != "" {
		expected := map[int]struct{}{}
		for _, part := range strings.Split(raw, ",") {
			code, err := strconv.Atoi(strings.TrimSpace(part))
			if err != nil {
				return CommandConfig{}, fmt.Errorf("invalid params.expected_exit: %w", err)
			}
			expected[code] = struct{}{}
		}
		cfg.ExpectedExit = expected
	}

	if raw := strings.TrimSpace(s.Params["expect"]); raw != "" {
		pattern, err := regexp.Compile("(?m)" + raw)
		if err != nil {
			return CommandConfig{}, fmt.Errorf("invalid params.expect: %w", err)
		}
		cfg.Expect = pattern
	}

	cfg.Command = command
	return cfg, nil
}

// outputTail keeps the last ~200 bytes of command output for the failure log.
func outputTail(output []byte) string {
	const tailLimit = 200
	if len(output) <= tailLimit {
		return string(output)
	}
	return string(output[len(output)-tailLimit:])
}
