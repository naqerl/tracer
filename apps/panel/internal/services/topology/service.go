package topology

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"

	"tracer/apps/panel/internal/domain"
	panelsvc "tracer/apps/panel/internal/services/panel"
	"tracer/libs/types"
)

var idSanitizer = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

type Snapshot struct {
	Panel      PanelNode   `json:"panel"`
	Collectors []Collector `json:"collectors"`
}

type PanelNode struct {
	ID string `json:"id"`
}

type Collector struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Apps []App  `json:"apps"`
}

type App struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	HasHealth bool   `json:"has_health"`
}

type Service struct {
	panel       *panelsvc.Service
	mu          sync.RWMutex
	nextID      int
	subscribers map[int]chan Message
}

type Message struct {
	Register  *types.RegisterEvent `json:"register,omitempty"`
	Envelope  *types.Envelope      `json:"envelope,omitempty"`
	Persisted *PersistedLogMessage `json:"persisted,omitempty"`
}

type PersistedLogMessage struct {
	Collector string `json:"collector"`
	App       string `json:"app"`
	Level     string `json:"level"`
	Timestamp string `json:"timestamp"`
}

func NewService(panel *panelsvc.Service) *Service {
	return &Service{
		panel:       panel,
		subscribers: make(map[int]chan Message),
	}
}

func (s *Service) Publish(evt Message) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, ch := range s.subscribers {
		select {
		case ch <- evt:
		default:
		}
	}
}

func (s *Service) Subscribe() (<-chan Message, func()) {
	ch := make(chan Message, 32)

	s.mu.Lock()
	id := s.nextID
	s.nextID++
	s.subscribers[id] = ch
	s.mu.Unlock()

	unsubscribe := func() {
		s.mu.Lock()
		defer s.mu.Unlock()

		current, ok := s.subscribers[id]
		if !ok {
			return
		}
		delete(s.subscribers, id)
		close(current)
	}

	return ch, unsubscribe
}

// GetSnapshot returns the topology visible to the actor. Only authenticated
// users have a user ID to scope collector/app listing against; the system and
// unauthenticated actors are denied.
func (s *Service) GetSnapshot(ctx context.Context, actor domain.Actor) (Snapshot, error) {
	var userID int64
	switch a := actor.(type) {
	case domain.User:
		userID = a.ID
	case domain.System:
		return Snapshot{}, panelsvc.ErrAccessDenied
	case domain.Unauthenticated:
		return Snapshot{}, panelsvc.ErrAccessDenied
	default:
		return Snapshot{}, domain.ErrUnexpectedActor
	}

	collectors, err := s.panel.ListCollectors(ctx, userID)
	if err != nil {
		return Snapshot{}, fmt.Errorf("list collectors: %w", err)
	}

	snapshot := Snapshot{
		Panel: PanelNode{
			ID: "panel-root",
		},
		Collectors: make([]Collector, 0, len(collectors)),
	}

	healthSince := time.Now().Add(-24 * time.Hour)
	for _, collector := range collectors {
		view := Collector{
			ID:   collectorID(collector.Name),
			Name: collector.Name,
		}

		apps, err := s.panel.ListCollectorApps(ctx, panelsvc.ListCollectorAppsRequest{
			UserID:        userID,
			CollectorName: collector.Name,
		})
		if err != nil {
			if errors.Is(err, panelsvc.ErrAccessDenied) {
				continue
			}
			return Snapshot{}, fmt.Errorf("list apps for %s: %w", collector.Name, err)
		}

		view.Apps = make([]App, 0, len(apps))
		for _, appName := range apps {
			appView := App{
				ID:   appID(collector.Name, appName),
				Name: appName,
			}

			health, err := s.panel.GetAppHealth(ctx, panelsvc.GetAppHealthRequest{
				UserID:        userID,
				CollectorName: collector.Name,
				AppName:       appName,
				Since:         healthSince,
			})
			if err == nil {
				appView.HasHealth = health.PeriodCount > 0
			} else if !errors.Is(err, panelsvc.ErrAppNotFound) && !errors.Is(err, panelsvc.ErrAccessDenied) {
				return Snapshot{}, fmt.Errorf("read health for %s/%s: %w", collector.Name, appName, err)
			}

			view.Apps = append(view.Apps, appView)
		}

		snapshot.Collectors = append(snapshot.Collectors, view)
	}

	return snapshot, nil
}

func collectorID(name string) string {
	return "collector-" + sanitizeID(name)
}

func appID(collectorName, appName string) string {
	return "app-" + sanitizeID(collectorName) + "-" + sanitizeID(appName)
}

func sanitizeID(raw string) string {
	safe := idSanitizer.ReplaceAllString(raw, "-")
	if safe == "" {
		return "node"
	}
	return safe
}
