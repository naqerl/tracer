package notify

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/ai-shift/tracer/apps/panel/db"
)

type Store struct {
	db      *sql.DB
	queries *db.Queries
}

func NewStore(sqlDB *sql.DB) *Store {
	return &Store{
		db:      sqlDB,
		queries: db.New(sqlDB),
	}
}

func (s *Store) GetNotifierDB(ctx context.Context, id int64) (*db.Notifier, error) {
	notifier, err := s.queries.GetNotifier(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("notifier not found")
		}
		return nil, err
	}
	return &notifier, nil
}

func (s *Store) GetNotifierByName(ctx context.Context, name string) (*db.Notifier, error) {
	notifier, err := s.queries.GetNotifierByName(ctx, name)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("notifier not found")
		}
		return nil, err
	}
	return &notifier, nil
}

func (s *Store) ListNotifiers(ctx context.Context) ([]db.Notifier, error) {
	return s.queries.ListNotifiers(ctx)
}

func (s *Store) ListNotifiersByUser(ctx context.Context, userID int64) ([]db.Notifier, error) {
	return s.queries.ListNotifiersByUser(ctx, userID)
}

func (s *Store) ListEnabledNotifiers(ctx context.Context) ([]db.Notifier, error) {
	return s.queries.ListEnabledNotifiers(ctx)
}

func (s *Store) UpdateNotifier(ctx context.Context, id int64, name, notifierType, config string, enabled bool) error {
	enabledInt := int64(0)
	if enabled {
		enabledInt = 1
	}
	return s.queries.UpdateNotifier(ctx, db.UpdateNotifierParams{
		ID:      id,
		Name:    name,
		Type:    notifierType,
		Config:  config,
		Enabled: enabledInt,
	})
}

func (s *Store) DeleteNotifier(ctx context.Context, id int64) error {
	return s.queries.DeleteNotifier(ctx, id)
}

func (s *Store) EnableNotifier(ctx context.Context, id int64) error {
	return s.queries.EnableNotifier(ctx, id)
}

func (s *Store) DisableNotifier(ctx context.Context, id int64) error {
	return s.queries.DisableNotifier(ctx, id)
}

func (s *Store) GetNotifierApps(ctx context.Context, notifierID int64) ([]NotifierAppSubscription, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT app_name, collector_name FROM notifier_app WHERE notifier_id = ?",
		notifierID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var subscriptions []NotifierAppSubscription
	for rows.Next() {
		var sub NotifierAppSubscription
		if err := rows.Scan(&sub.AppName, &sub.CollectorName); err != nil {
			return nil, err
		}
		subscriptions = append(subscriptions, sub)
	}

	return subscriptions, rows.Err()
}

func (s *Store) AddNotifierApp(ctx context.Context, notifierID int64, appName string) error {
	return s.queries.AddNotifierApp(ctx, db.AddNotifierAppParams{
		NotifierID: notifierID,
		AppName:    appName,
	})
}

func (s *Store) RemoveNotifierApp(ctx context.Context, notifierID int64, appName string) error {
	return s.queries.RemoveNotifierApp(ctx, db.RemoveNotifierAppParams{
		NotifierID: notifierID,
		AppName:    appName,
	})
}

func (s *Store) SetNotifierApps(ctx context.Context, notifierID int64, subscriptions []NotifierAppSubscription) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, "DELETE FROM notifier_app WHERE notifier_id = ?", notifierID)
	if err != nil {
		return fmt.Errorf("delete old apps: %w", err)
	}

	for _, sub := range subscriptions {
		if sub.AppName == "" {
			return fmt.Errorf("app_name cannot be empty")
		}
		if sub.CollectorName == "" {
			return fmt.Errorf("collector_name cannot be empty (use '*' for wildcard)")
		}

		_, err = tx.ExecContext(ctx,
			"INSERT INTO notifier_app (notifier_id, app_name, collector_name) VALUES (?, ?, ?)",
			notifierID, sub.AppName, sub.CollectorName)
		if err != nil {
			return fmt.Errorf("insert app: %w", err)
		}
	}

	return tx.Commit()
}

func (s *Store) GetNotifiersForApp(ctx context.Context, appName string) ([]db.Notifier, error) {
	return s.queries.GetNotifiersForApp(ctx, appName)
}

func (s *Store) GetGlobalNotifiers(ctx context.Context) ([]db.Notifier, error) {
	return s.queries.GetGlobalNotifiers(ctx)
}

func (s *Store) CreateNotifier(ctx context.Context, userID int64, name, notifierType, config string, enabled bool) (NotifierData, error) {
	enabledInt := int64(0)
	if enabled {
		enabledInt = 1
	}

	notifier, err := s.queries.CreateNotifier(ctx, db.CreateNotifierParams{
		UserID:  userID,
		Name:    name,
		Type:    notifierType,
		Config:  config,
		Enabled: enabledInt,
	})
	if err != nil {
		return NotifierData{}, err
	}

	return NotifierData{
		ID:      notifier.ID,
		UserID:  notifier.UserID,
		Name:    notifier.Name,
		Type:    notifier.Type,
		Config:  notifier.Config,
		Enabled: notifier.Enabled == 1,
	}, nil
}

func (s *Store) GetNotifier(ctx context.Context, id int64) (NotifierData, error) {
	notifier, err := s.queries.GetNotifier(ctx, id)
	if err != nil {
		return NotifierData{}, err
	}

	return NotifierData{
		ID:      notifier.ID,
		UserID:  notifier.UserID,
		Name:    notifier.Name,
		Type:    notifier.Type,
		Config:  notifier.Config,
		Enabled: notifier.Enabled == 1,
	}, nil
}
