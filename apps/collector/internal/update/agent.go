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
	ctx    context.Context
	cancel context.CancelFunc
}

// NewAgent creates an Agent for the given collector identity.
// The caller should provide a parent context (e.g. the WebSocket recv loop
// context) so that in-flight downloads are cancelled when the connection drops.
func NewAgent(ctx context.Context, name string, env updatex.Environment, client sender.Client) *Agent {
	ctx, cancel := context.WithCancel(ctx)
	return &Agent{
		name:   name,
		env:    env,
		client: client,
		ctx:    ctx,
		cancel: cancel,
	}
}

// Close cancels the agent's context, aborting any in-flight prepare or activate
// operation. The caller should call Close when the WebSocket connection is lost
// or when the agent is no longer needed.
func (a *Agent) Close() {
	a.cancel()
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
func (a *Agent) SendHello(hasPending bool) error {
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
func (a *Agent) sendStatus(status, step, errMsg, oldVer, newVer string) {
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
	ctx, cancel := context.WithTimeout(a.ctx, 5*time.Minute)
	defer cancel()

	a.sendStatus("preparing", "download", "", "", "")

	var cmd types.UpdatePrepareCommand
	if err := env.DecodeData(&cmd); err != nil {
		slog.Error("could not decode update prepare command", "error", err)
		a.sendStatus("failed", "decode", err.Error(), "", "")
		return
	}

	slog.Info("preparing update", "version", cmd.Version)

	stagedPath, err := updatex.UpdateBinary(ctx, a.env, cmd.DownloadURL, cmd.Checksum)
	if err != nil {
		slog.Error("could not prepare update", "error", err)
		a.sendStatus("failed", "download", err.Error(), "", cmd.Version)
		return
	}

	a.sendStatus("prepared", "staged", "", "", cmd.Version)
	slog.Info("update prepared successfully", "version", cmd.Version, "path", stagedPath)
}

// handleActivate processes an UpdateActivateCommand.
func (a *Agent) handleActivate(env types.Envelope) {
	ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
	defer cancel()

	a.sendStatus("activating", "activate", "", "", "")

	// Get the old version before activation (for status reporting).
	oldVersion, _ := a.env.VersionOf(ctx, a.env.BinaryPath())

	slog.Info("activating update")

	if err := updatex.ActivateStaged(ctx, a.env); err != nil {
		slog.Error("could not activate update", "error", err)
		a.sendStatus("failed", "activate", err.Error(), oldVersion, "")
		return
	}

	// Get the new version after activation.
	newVersion, _ := a.env.VersionOf(ctx, a.env.BinaryPath())

	a.sendStatus("activated", "done", "", oldVersion, newVersion)
	slog.Info("update activated successfully", "old", oldVersion, "new", newVersion)
}

// CheckPendingUpdate returns true if a staged binary exists.
func (a *Agent) CheckPendingUpdate() bool {
	_, ok := updatex.StagedVersion(a.env)
	return ok
}
