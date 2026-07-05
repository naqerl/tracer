package update

import (
	"sync"

	"tracer/libs/types"
)

// Stage names emitted in progress events.
const (
	StageDownload = "download"
	StageBuild    = "build"
	StageVerify   = "verify"
	StageInstall  = "install"
	StageRestart  = "restart"
	StageComplete = "complete"
)

// Status constants exported for convenience. These alias the types package
// values so that update package users don't need to import types separately.
const (
	StatusInProgress = types.ProgressInProgress
	StatusDone       = types.ProgressDone
	StatusFailed     = types.ProgressFailed
)

// ProgressEvent carries the status of a single update stage.
type ProgressEvent struct {
	Stage   string               `json:"stage"`
	Status  types.ProgressStatus `json:"status"`
	Message string               `json:"message,omitempty"`
}

// Report provides a channel of progress events from a running self-update.
// Call Wait() to block until the update completes and get any error.
type Report struct {
	Events <-chan ProgressEvent
	done   chan struct{}
	err    error
	once   sync.Once
}

// Wait blocks until the update finishes and returns any error.
func (r *Report) Wait() error {
	<-r.done
	return r.err
}

// setErr records the final error and signals completion.
func (r *Report) setErr(err error) {
	r.once.Do(func() {
		r.err = err
		close(r.done)
	})
}
