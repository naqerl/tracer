package permissions

import (
	"context"

	"github.com/ai-shift/tracer/apps/panel/internal/services/ingest"
	"github.com/ai-shift/tracer/apps/panel/internal/services/user"
	sharedtypes "github.com/ai-shift/tracer/libs/types"
)

type Service struct {
	userService *user.Service
	tracker     *ingest.Tracker
}

func New(userService *user.Service, tracker *ingest.Tracker) *Service {
	return &Service{
		userService: userService,
		tracker:     tracker,
	}
}

func (s *Service) IsAdmin(user sharedtypes.User) bool {
	return user.IsAdmin
}

func (s *Service) CanAccessApp(ctx context.Context, user sharedtypes.User, appName string) (bool, error) {
	return s.userService.HasAppAccess(ctx, user.ID, appName)
}

func (s *Service) CanAccessCollector(ctx context.Context, user sharedtypes.User, collectorName string) (bool, error) {
	if s.IsAdmin(user) {
		return true, nil
	}
	if user.ID == 0 || s.tracker == nil {
		return false, nil
	}

	collector, exists := s.tracker.Get(collectorName)
	if !exists {
		return false, nil
	}

	userApps, err := s.userService.GetApps(ctx, user.ID)
	if err != nil {
		return false, err
	}
	userAppSet := make(map[string]struct{}, len(userApps))
	for _, app := range userApps {
		userAppSet[app] = struct{}{}
	}

	for _, app := range collector.Apps {
		if _, ok := userAppSet[app]; ok {
			return true, nil
		}
	}

	return false, nil
}

func (s *Service) FilterAppsByAccess(ctx context.Context, user sharedtypes.User, apps []string) ([]string, error) {
	if s.IsAdmin(user) {
		return apps, nil
	}
	if user.ID == 0 {
		return nil, nil
	}

	userApps, err := s.userService.GetApps(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	userAppSet := make(map[string]struct{}, len(userApps))
	for _, app := range userApps {
		userAppSet[app] = struct{}{}
	}

	filtered := make([]string, 0, len(apps))
	for _, app := range apps {
		if _, ok := userAppSet[app]; ok {
			filtered = append(filtered, app)
		}
	}
	return filtered, nil
}

func (s *Service) CanManageNotifier(user sharedtypes.User, notifierOwnerID int64) bool {
	return s.IsAdmin(user) || user.ID == notifierOwnerID
}
