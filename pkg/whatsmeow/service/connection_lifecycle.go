package whatsmeow_service

import (
	"context"
	"database/sql"
	"sync"

	"go.mau.fi/whatsmeow/store/sqlstore"
)

// Reuse the application's bounded authentication pool across every instance
// and socket generation. sqlstore.New would open an unbounded independent pool.
type sharedSessionStore struct {
	mu        sync.Mutex
	container *sqlstore.Container
	ready     bool
}

func newSharedSessionStore(db *sql.DB) *sharedSessionStore {
	return &sharedSessionStore{container: sqlstore.NewWithDB(db, "postgres", nil)}
}

func (s *sharedSessionStore) get(ctx context.Context) (*sqlstore.Container, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.ready {
		if err := s.container.Upgrade(ctx); err != nil {
			return nil, err
		}
		s.ready = true
	}
	return s.container, nil
}

// Buffered, generation-specific cancellation also works while Connect is in
// progress. A repeated cancellation neither blocks event dispatch nor restarts.
func signalClientStop(stop chan bool) {
	select {
	case stop <- true:
	default:
	}
}
