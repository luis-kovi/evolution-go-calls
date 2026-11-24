package whatsmeow_service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waWa6"
	"go.mau.fi/whatsmeow/store"
)

var (
	compiledVersion = store.WAVersionContainer{2, 3000, 1043636855}
	webVersion      = store.WAVersionContainer{2, 3000, 1054751086}
)

type fakeFetcher struct {
	mu      sync.Mutex
	version store.WAVersionContainer
	err     error
	calls   int
}

func (f *fakeFetcher) fetch(context.Context) (store.WAVersionContainer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.version, f.err
}

func (f *fakeFetcher) set(version store.WAVersionContainer, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.version, f.err = version, err
}

func (f *fakeFetcher) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func TestWAVersionFollowsWhatsAppWeb(t *testing.T) {
	fetcher := &fakeFetcher{version: webVersion}
	r := newWAVersionResolver(compiledVersion, store.WAVersionContainer{}, fetcher.fetch)
	if got := r.current(); got != compiledVersion {
		t.Fatalf("before lookup got %s", got.String())
	}
	got, err := r.refresh(context.Background(), false)
	if err != nil || got != webVersion || r.current() != webVersion {
		t.Fatalf("got %s, %v", got.String(), err)
	}
}

func TestWAVersionNeverGoesBelowCompiledOrSeen(t *testing.T) {
	fetcher := &fakeFetcher{version: store.WAVersionContainer{2, 3000, 1000000000}}
	r := newWAVersionResolver(compiledVersion, store.WAVersionContainer{}, fetcher.fetch)
	if got, _ := r.refresh(context.Background(), true); got != compiledVersion {
		t.Fatalf("downgraded below compiled: %s", got.String())
	}
	fetcher.set(webVersion, nil)
	r.refresh(context.Background(), true)
	fetcher.set(store.WAVersionContainer{2, 3000, 1050000000}, nil)
	if got, _ := r.refresh(context.Background(), true); got != webVersion {
		t.Fatalf("downgraded below a seen revision: %s", got.String())
	}
}

func TestWAVersionKeepsPreviousOnLookupFailure(t *testing.T) {
	fetcher := &fakeFetcher{version: webVersion}
	r := newWAVersionResolver(compiledVersion, store.WAVersionContainer{}, fetcher.fetch)
	r.refresh(context.Background(), true)
	fetcher.set(store.WAVersionContainer{}, errors.New("blocked"))
	got, err := r.refresh(context.Background(), true)
	if err == nil || got != webVersion {
		t.Fatalf("got %s, %v", got.String(), err)
	}
	fetcher.set(store.WAVersionContainer{}, nil)
	if got, err := r.refresh(context.Background(), true); err == nil || got != webVersion {
		t.Fatalf("empty answer accepted: %s, %v", got.String(), err)
	}
}

func TestWAVersionLookupIsCachedUnlessForced(t *testing.T) {
	fetcher := &fakeFetcher{version: webVersion}
	r := newWAVersionResolver(compiledVersion, store.WAVersionContainer{}, fetcher.fetch)
	now := time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }

	r.refresh(context.Background(), false)
	r.refresh(context.Background(), false)
	if fetcher.count() != 1 {
		t.Fatalf("cached lookup repeated: %d", fetcher.count())
	}
	r.refresh(context.Background(), true)
	if fetcher.count() != 2 {
		t.Fatalf("forced lookup skipped: %d", fetcher.count())
	}
	now = now.Add(waVersionRefreshInterval)
	r.refresh(context.Background(), false)
	if fetcher.count() != 3 {
		t.Fatalf("expired lookup skipped: %d", fetcher.count())
	}
	// A failure also counts as a check, so StartClient does not hammer the web.
	fetcher.set(store.WAVersionContainer{}, errors.New("down"))
	now = now.Add(waVersionRefreshInterval)
	r.refresh(context.Background(), false)
	r.refresh(context.Background(), false)
	if fetcher.count() != 4 {
		t.Fatalf("failed lookup retried before the interval: %d", fetcher.count())
	}
}

func TestWAVersionOverrideWinsWithoutLookup(t *testing.T) {
	fetcher := &fakeFetcher{version: webVersion}
	override := waVersionOverride(2, 3000, 1099999999)
	r := newWAVersionResolver(compiledVersion, override, fetcher.fetch)
	if got, err := r.refresh(context.Background(), true); err != nil || got != override || r.current() != override {
		t.Fatalf("got %s, %v", got.String(), err)
	}
	if fetcher.count() != 0 {
		t.Fatal("override must not query WhatsApp Web")
	}
	if !waVersionOverride(2, 0, 1).IsZero() || !waVersionOverride(0, 0, 0).IsZero() {
		t.Fatal("partial override accepted")
	}
}

func TestApplyWAVersionStampsHandshakePayload(t *testing.T) {
	login := &waWa6.ClientPayload{UserAgent: &waWa6.ClientPayload_UserAgent{AppVersion: compiledVersion.ProtoAppVersion()}}
	applyWAVersion(login, webVersion)
	if got := login.GetUserAgent().GetAppVersion(); got.GetTertiary() != webVersion[2] || got.GetSecondary() != webVersion[1] || got.GetPrimary() != webVersion[0] {
		t.Fatalf("login payload kept %v", got)
	}

	registration := &waWa6.ClientPayload{
		UserAgent:         &waWa6.ClientPayload_UserAgent{},
		DevicePairingData: &waWa6.ClientPayload_DevicePairingRegistrationData{},
	}
	applyWAVersion(registration, webVersion)
	hash := webVersion.Hash()
	if string(registration.GetDevicePairingData().GetBuildHash()) != string(hash[:]) {
		t.Fatal("registration build hash does not match the revision")
	}
	applyWAVersion(nil, webVersion) // must not panic
}

func TestWAVersionConcurrentReadsAndRefreshes(t *testing.T) {
	fetcher := &fakeFetcher{version: webVersion}
	r := newWAVersionResolver(compiledVersion, store.WAVersionContainer{}, fetcher.fetch)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); r.refresh(context.Background(), true) }()
		go func() {
			defer wg.Done()
			if got := r.current(); got != compiledVersion && got != webVersion {
				t.Errorf("torn version %s", got.String())
			}
		}()
	}
	wg.Wait()
}

type fakeOutdatedClient struct {
	loggedIn  atomic.Bool
	connects  atomic.Int32
	onConnect func()
}

func (c *fakeOutdatedClient) IsLoggedIn() bool { return c.loggedIn.Load() }
func (c *fakeOutdatedClient) Connect() error {
	c.connects.Add(1)
	if c.onConnect != nil {
		c.onConnect()
	}
	return nil
}

func testRecovery(r *waVersionResolver, client outdatedClient, done <-chan struct{}, attempts int) outdatedRecovery {
	delays := make([]time.Duration, attempts)
	for i := range delays {
		delays[i] = time.Millisecond
	}
	noop := func(string, ...any) {}
	return outdatedRecovery{versions: r, client: client, done: done, delays: delays, settle: time.Millisecond, logInfo: noop, logError: noop}
}

func TestOutdatedRecoveryReconnectsWithNewerVersion(t *testing.T) {
	fetcher := &fakeFetcher{version: webVersion}
	r := newWAVersionResolver(compiledVersion, store.WAVersionContainer{}, fetcher.fetch)
	client := &fakeOutdatedClient{}
	client.onConnect = func() {
		if r.current() != webVersion {
			t.Errorf("reconnected with %s", r.current().String())
		}
		client.loggedIn.Store(true)
	}
	if !testRecovery(r, client, make(chan struct{}), 3).run(compiledVersion) {
		t.Fatal("session not recovered")
	}
	if client.connects.Load() != 1 {
		t.Fatalf("connects = %d", client.connects.Load())
	}
}

func TestOutdatedRecoveryDoesNotRetryTheRejectedVersion(t *testing.T) {
	fetcher := &fakeFetcher{version: compiledVersion}
	r := newWAVersionResolver(compiledVersion, store.WAVersionContainer{}, fetcher.fetch)
	client := &fakeOutdatedClient{}
	if testRecovery(r, client, make(chan struct{}), 3).run(compiledVersion) {
		t.Fatal("reported recovery without a newer version")
	}
	if client.connects.Load() != 0 {
		t.Fatalf("reconnected with the rejected version %d times", client.connects.Load())
	}
	if fetcher.count() != 3 {
		t.Fatalf("lookups = %d", fetcher.count())
	}
}

func TestOutdatedRecoveryUsesVersionAlreadyRefreshedByAnotherInstance(t *testing.T) {
	// Another instance already moved the resolver to the web revision; this
	// client was rejected with the compiled one and must still reconnect.
	fetcher := &fakeFetcher{version: webVersion}
	r := newWAVersionResolver(compiledVersion, store.WAVersionContainer{}, fetcher.fetch)
	r.refresh(context.Background(), true)
	client := &fakeOutdatedClient{}
	client.onConnect = func() { client.loggedIn.Store(true) }
	if !testRecovery(r, client, make(chan struct{}), 2).run(compiledVersion) || client.connects.Load() != 1 {
		t.Fatalf("connects = %d", client.connects.Load())
	}
}

func TestOutdatedRecoveryStopsWithItsGeneration(t *testing.T) {
	fetcher := &fakeFetcher{version: webVersion}
	r := newWAVersionResolver(compiledVersion, store.WAVersionContainer{}, fetcher.fetch)
	done := make(chan struct{})
	close(done)
	client := &fakeOutdatedClient{}
	if testRecovery(r, client, done, 3).run(compiledVersion) || client.connects.Load() != 0 {
		t.Fatal("recovery ran after its generation ended")
	}
}

func TestOutdatedWebhookIsAConnectFailureWith405(t *testing.T) {
	data := outdatedWebhookData(compiledVersion)
	if data["Reason"] != 405 || data["clientVersion"] != "2.3000.1043636855" {
		t.Fatalf("unexpected payload %v", data)
	}
}
