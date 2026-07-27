// Package update implements the collector-side update agent that receives
// update commands from the panel and executes binary download/activation.
package update

import (
	"context"
	"log/slog"
	"time"

	"tracer/apps/collector/internal/sender"
	"tracer/build"
	"tracer/libs/types"
	"tracer/libs/updatex"
)

// Agent handles update commands from the panel. It registers handlers on a
// Router and uses a sender.Client to send status reports back to the panel.
type Agent struct {
	name   string
	env    updatex.Environment
	client sender.Client
}

// NewAgent creates an Agent for the given collector identity.
func NewAgent(name string, env updatex.Environment, client sender.Client) *Agent {
	return &Agent{
		name:   name,
		env:    env,
		client: client,
	}
}

// Register registers the agent's handlers on the router.
// The agent handles:
//   - EventTypeUpdatePrepare: download and stage a new binary
//   - EventTypeUpdateActivate: activate the staged binary
func (a *Agent) Register(r *sender.Router) {
	r.Register(types.EventTypeUpdatePrepare, a.handlePrepare)
	r.Register(types.EventTypeUpdateActivate, a.handleActivate)
}

// SendHello sends a HelloReport to the panel on reconnect. This should be
// called when the WebSocket connection is established (e.g., in the factory
// callback before returning the client, or as the first message after connect).
func (a *Agent) SendHello(ctx context.Context, hasPending bool) error {
	version := build.Commit
	hello := types.HelloReport{
		Type:             types.EventTypeHello,
		Collector:        a.name,
		Version:          version,
		HasPendingUpdate: hasPending,
	}
	env := types.Envelope{
		Type:      hello.EventType(),
		Time:      time.Now().UTC(),
		Collector: a.name,
		Data:      hello,
	}
	return a.client.Send(env)
}

// sendStatus sends an UpdateStatusReport to the panel.
func (a *Agent) sendStatus(ctx context.Context, status, step, errMsg, oldVer, newVer string) {
	report := types.UpdateStatusReport{
		Type:       types.EventTypeUpdateStatus,
		Collector:  a.name,
		Status:     status,
		Step:       step,
		Error:      errMsg,
		OldVersion: oldVer,
		NewVersion: newVer,
	}
	env := types.Envelope{
		Type:      report.EventType(),
		Time:      time.Now().UTC(),
		Collector: a.name,
		Data:      report,
	}
	if sendErr := a.client.Send(env); sendErr != nil {
		slog.Warn("could not send update status", "status", status, "error", sendErr)
	}
}

// handlePrepare processes an UpdatePrepareCommand.
func (a *Agent) handlePrepare(env types.Envelope) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	a.sendStatus(ctx, "preparing", "download", "", "", "")

	var cmd types.UpdatePrepareCommand
	if err := env.DecodeData(&cmd); err != nil {
		slog.Error("could not decode update prepare command", "error", err)
		a.sendStatus(ctx, "failed", "decode", err.Error(), "", "")
		return
	}

	slog.Info("preparing update", "version", cmd.Version)

	stagedPath, err := updatex.UpdateBinary(ctx, a.env, cmd.DownloadURL, cmd.Checksum)
	if err != nil {
		slog.Error("could not prepare update", "error", err)
		a.sendStatus(ctx, "failed", "download", err.Error(), "", cmd.Version)
		return
	}

	a.sendStatus(ctx, "prepared", "staged", "", "", cmd.Version)
	slog.Info("update prepared successfully", "version", cmd.Version, "path", stagedPath)
}

// handleActivate processes an UpdateActivateCommand.
func (a *Agent) handleActivate(env types.Envelope) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	a.sendStatus(ctx, "activating", "activate", "", "", "")

	// Get the old version before activation (for status reporting).
	oldVersion, _ := a.env.VersionOf(ctx, a.env.BinaryPath())

	slog.Info("activating update")

	if err := updatex.ActivateStaged(ctx, a.env); err != nil {
		slog.Error("could not activate update", "error", err)
		a.sendStatus(ctx, "failed", "activate", err.Error(), oldVersion, "")
		return
	}

	// Get the new version after activation.
	newVersion, _ := a.env.VersionOf(ctx, a.env.BinaryPath())

	a.sendStatus(ctx, "activated", "done", "", oldVersion, newVersion)
	slog.Info("update activated successfully", "old", oldVersion, "new", newVersion)
}

// CheckPendingUpdate returns true if a staged binary exists.
func (a *Agent) CheckPendingUpdate() bool {
	_, ok := updatex.StagedVersion(a.env)
	return ok
}
