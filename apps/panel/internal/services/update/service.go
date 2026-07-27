package update

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"tracer/apps/panel/db"
	"tracer/apps/panel/internal/dbmanager"
	"tracer/libs/types"
	"tracer/libs/updatex"
)

// CollectorClient is the interface for sending commands to connected collectors.
type CollectorClient interface {
	SendTo(ctx context.Context, name string, msg types.Event) error
	ConnectedNames() []string
}

// Service orchestrates binary updates across collectors and the panel itself.
type Service struct {
	db         *dbmanager.DBHandle
	collectors CollectorClient
	env        updatex.Environment
	tracker    *Tracker
}

// New creates an update Service. It requires a panel database handle, a
// CollectorClient for sending commands to collectors, and an Environment
// for self-update operations.
func New(db *dbmanager.DBHandle, collectors CollectorClient, env updatex.Environment) *Service {
	return &Service{
		db:         db,
		collectors: collectors,
		env:        env,
		tracker:    NewTracker(),
	}
}

// Tracker returns the in-memory tracker for progress observation.
func (s *Service) Tracker() *Tracker { return s.tracker }

// ListPlans returns all update plans from the database.
func (s *Service) ListPlans(ctx context.Context) ([]Plan, error) {
	rows, err := s.db.Read().QueryContext(ctx,
		`SELECT id, version, status, created_at, completed_at
		 FROM update_plans ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("could not list update plans: %w", err)
	}
	defer rows.Close()

	var plans []Plan
	for rows.Next() {
		var p Plan
		var createdAt int64
		var completedAt sql.NullInt64
		if err := rows.Scan(&p.ID, &p.Version, &p.Status, &createdAt, &completedAt); err != nil {
			return nil, fmt.Errorf("could not scan plan row: %w", err)
		}
		p.CreatedAt = time.Unix(createdAt, 0)
		if completedAt.Valid {
			t := time.Unix(completedAt.Int64, 0)
			p.CompletedAt = &t
		}
		plans = append(plans, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration error: %w", err)
	}
	if plans == nil {
		plans = []Plan{}
	}
	return plans, nil
}

// GetPlan returns a single plan with its targets.
func (s *Service) GetPlan(ctx context.Context, id string) (*Plan, []Target, error) {
	var p Plan
	var createdAt int64
	var completedAt sql.NullInt64
	err := s.db.Read().QueryRowContext(ctx,
		`SELECT id, version, status, created_at, completed_at
		 FROM update_plans WHERE id = ?`, id).Scan(&p.ID, &p.Version, &p.Status, &createdAt, &completedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("could not get plan %s: %w", id, err)
	}
	p.CreatedAt = time.Unix(createdAt, 0)
	if completedAt.Valid {
		t := time.Unix(completedAt.Int64, 0)
		p.CompletedAt = &t
	}

	targetRows, err := s.db.Read().QueryContext(ctx,
		`SELECT id, plan_id, collector_id, target_name, status, step, error,
		        old_version, new_version, binary_digest, started_at, completed_at
		 FROM update_targets WHERE plan_id = ? ORDER BY target_name`, id)
	if err != nil {
		return nil, nil, fmt.Errorf("could not get targets for plan %s: %w", id, err)
	}
	defer targetRows.Close()

	var targets []Target
	for targetRows.Next() {
		var t Target
		var startedAt, compAt sql.NullInt64
		if err := targetRows.Scan(&t.ID, &t.PlanID, &t.CollectorID, &t.TargetName,
			&t.Status, &t.Step, &t.Error, &t.OldVersion, &t.NewVersion,
			&t.BinaryDigest, &startedAt, &compAt); err != nil {
			return nil, nil, fmt.Errorf("could not scan target row: %w", err)
		}
		if startedAt.Valid {
			ts := time.Unix(startedAt.Int64, 0)
			t.StartedAt = &ts
		}
		if compAt.Valid {
			ts := time.Unix(compAt.Int64, 0)
			t.CompletedAt = &ts
		}
		targets = append(targets, t)
	}
	if err := targetRows.Err(); err != nil {
		return nil, nil, fmt.Errorf("target rows iteration error: %w", err)
	}
	if targets == nil {
		targets = []Target{}
	}

	return &p, targets, nil
}

// StartPlan creates a new update plan and begins the prepare phase.
// It sends UpdatePrepareCommand to all connected collectors.
func (s *Service) StartPlan(ctx context.Context, version, downloadURL, checksum string) (*Plan, error) {
	planID := uuid.New().String()
	now := time.Now()

	// Create plan in DB.
	err := s.db.Write(ctx, func(tx db.DBTX) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO update_plans (id, version, status, created_at)
			 VALUES (?, ?, 'preparing', ?)`,
			planID, version, now.Unix())
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("could not create update plan: %w", err)
	}

	plan := &Plan{
		ID:        planID,
		Version:   version,
		Status:    StatusPreparing,
		CreatedAt: now,
	}

	s.tracker.PublishPlanStatus(planID, StatusPreparing)

	// Add self as a target.
	collectorNames := s.collectors.ConnectedNames()

	selfTargetID := uuid.New().String()
	err = s.db.Write(ctx, func(tx db.DBTX) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO update_targets (id, plan_id, target_name, status)
			 VALUES (?, ?, 'self', 'pending')`,
			selfTargetID, planID)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("could not create self target: %w", err)
	}

	for _, name := range collectorNames {
		targetID := uuid.New().String()
		err = s.db.Write(ctx, func(tx db.DBTX) error {
			_, err := tx.ExecContext(ctx,
				`INSERT INTO update_targets (id, plan_id, target_name, status)
				 VALUES (?, ?, ?, 'pending')`,
				targetID, planID, name)
			return err
		})
		if err != nil {
			return nil, fmt.Errorf("could not create target for collector %s: %w", name, err)
		}
	}

	// Send prepare command to all connected collectors.
	cmd := types.UpdatePrepareCommand{
		Type:        types.EventTypeUpdatePrepare,
		Version:     version,
		DownloadURL: downloadURL,
		Checksum:    checksum,
	}

	for _, name := range collectorNames {
		if err := s.collectors.SendTo(ctx, name, cmd); err != nil {
			slog.Error("could not send prepare command", "collector", name, "error", err)
		}
	}

	slog.Info("update plan started", "plan_id", planID, "version", version, "targets", len(collectorNames))

	return plan, nil
}

// CancelPlan cancels an in-progress plan.
// Returns ErrPlanNotActive if the plan is not in a cancellable state.
func (s *Service) CancelPlan(ctx context.Context, id string) error {
	now := time.Now()
	err := s.db.Write(ctx, func(tx db.DBTX) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE update_plans SET status = 'cancelled', completed_at = ?
			 WHERE id = ? AND status IN ('pending', 'preparing', 'activating')`,
			now.Unix(), id)
		if err != nil {
			return err
		}
		rows, _ := res.RowsAffected()
		if rows == 0 {
			return ErrPlanNotActive
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrPlanNotActive) {
			return err
		}
		return fmt.Errorf("could not cancel plan %s: %w", id, err)
	}

	s.tracker.PublishPlanStatus(id, StatusCancelled)
	return nil
}

// ErrPlanNotActive is returned when trying to cancel a plan that is not in
// a cancellable state (pending, preparing, or activating).
var ErrPlanNotActive = errors.New("plan is not in a cancellable state")

// IngestUpdateStatus processes an UpdateStatusReport from a collector.
func (s *Service) IngestUpdateStatus(ctx context.Context, report types.UpdateStatusReport) error {
	// Find the active plan that includes this collector target.
	planID, err := s.findActivePlanForTarget(ctx, report.Collector)
	if err != nil {
		return fmt.Errorf("could not find active plan for collector %s: %w", report.Collector, err)
	}
	if planID == "" {
		// No active plan — likely a stale status report.
		return nil
	}

	// Update target status in DB.
	now := time.Now()
	err = s.db.Write(ctx, func(tx db.DBTX) error {
		var completedAt *int64
		if report.Status == StatusPrepared || report.Status == StatusActivated || report.Status == StatusFailed {
			unix := now.Unix()
			completedAt = &unix
		}
		_, err := tx.ExecContext(ctx,
			`UPDATE update_targets
			 SET status = ?, step = ?, error = ?, old_version = ?, new_version = ?,
			     completed_at = COALESCE(?, completed_at)
			 WHERE plan_id = ? AND target_name = ?`,
			report.Status, report.Step, report.Error,
			report.OldVersion, report.NewVersion,
			completedAt, planID, report.Collector)
		return err
	})
	if err != nil {
		return fmt.Errorf("could not update target status: %w", err)
	}

	s.tracker.IngestUpdateStatus(planID, report.Collector, report)
	return nil
}

// IngestHello checks if a reconnecting collector has an active update and marks
// it as completed if the previous status was "activating" or "restarting".
//
// Note: After a successful activation, the staged binary is renamed into the
// canonical path, so hello.HasPendingUpdate is false. We match purely on the
// target's status, not on HasPendingUpdate.
func (s *Service) IngestHello(ctx context.Context, hello types.HelloReport) error {
	planID, err := s.findActivePlanForTarget(ctx, hello.Collector)
	if err != nil {
		return fmt.Errorf("could not find active plan for collector %s: %w", hello.Collector, err)
	}
	if planID == "" {
		return nil
	}

	err = s.db.Write(ctx, func(tx db.DBTX) error {
		now := time.Now().Unix()
		res, err := tx.ExecContext(ctx,
			`UPDATE update_targets
			 SET status = 'activated', step = 'done', completed_at = ?
			 WHERE plan_id = ? AND target_name = ? AND status IN ('activating', 'restarting')`,
			now, planID, hello.Collector)
		if err != nil {
			return err
		}
		rows, _ := res.RowsAffected()
		if rows > 0 {
			s.tracker.PublishTargetStatus(planID, &Target{
				TargetName: hello.Collector,
				Status:     StatusActivated,
				Step:       "done",
			})
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("could not update target after hello: %w", err)
	}
	return nil
}

// SelfPrepare downloads and stages an update for the panel binary itself.
func (s *Service) SelfPrepare(ctx context.Context, url, checksum string) error {
	slog.Info("preparing self-update")
	_, err := updatex.UpdateBinary(ctx, s.env, url, checksum)
	if err != nil {
		return fmt.Errorf("could not prepare self-update: %w", err)
	}
	return nil
}

// SelfActivate activates the staged panel binary.
func (s *Service) SelfActivate(ctx context.Context) error {
	slog.Info("activating self-update")
	return updatex.ActivateStaged(ctx, s.env)
}

// findActivePlanForTarget returns the ID of the active plan that includes the
// given target name. Returns ("", nil) if no active plan exists.
func (s *Service) findActivePlanForTarget(ctx context.Context, targetName string) (string, error) {
	var planID sql.NullString
	err := s.db.Read().QueryRowContext(ctx,
		`SELECT p.id FROM update_plans p
		 INNER JOIN update_targets t ON t.plan_id = p.id
		 WHERE p.status IN ('pending', 'preparing', 'activating')
		   AND t.target_name = ?
		 ORDER BY p.created_at DESC LIMIT 1`, targetName).Scan(&planID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return planID.String, nil
}
