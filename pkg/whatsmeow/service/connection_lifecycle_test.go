package whatsmeow_service

import (
	"context"
	"sync"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestSessionStoreReusesBoundedPoolAcrossReconnects(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(2)
	s := newSharedSessionStore(db)
	// Schema has already been upgraded by startup. Hundreds of reconnects must
	// reuse this container and perform no schema checks or open new pools.
	s.ready = true
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			container, err := s.get(context.Background())
			if err != nil || container != s.container {
				t.Errorf("store changed: %v", err)
			}
		}()
	}
	wg.Wait()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if db.Stats().MaxOpenConnections != 2 {
		t.Fatal("pool limit changed")
	}
}

func TestOldQRTimerCannotStopReplacementClient(t *testing.T) {
	oldStop, newStop := make(chan bool, 1), make(chan bool, 1)
	signalClientStop(oldStop)
	signalClientStop(oldStop) // duplicate must not block
	select {
	case <-newStop:
		t.Fatal("replacement stopped")
	default:
	}
	select {
	case <-oldStop:
	default:
		t.Fatal("cancellation lost during startup")
	}
}
