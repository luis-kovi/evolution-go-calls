package whatsmeow_service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waWa6"
	"go.mau.fi/whatsmeow/store"
)

// WhatsApp rejects handshakes from an old WhatsApp Web revision with a 405
// connect failure. whatsmeow then disables its automatic reconnect and only
// dispatches events.ClientOutdated, so an outdated build stays offline after
// the next routine socket drop. On 2026-11-23 this took every instance down.
//
// The revision used for each handshake therefore comes from WhatsApp Web at
// runtime, never below the one compiled into whatsmeow. It is applied per
// client on the cloned handshake payload (Client.GetClientPayload) instead of
// store.SetWAVersion, which rewrites package globals that concurrent
// handshakes of other instances read without a lock.

const (
	waVersionRefreshInterval = 30 * time.Minute
	waVersionFetchTimeout    = 15 * time.Second
)

// Delays between recovery attempts after a 405. WhatsApp usually publishes the
// new revision before rejecting the old one, so the first attempt is quick.
var outdatedRecoveryDelays = []time.Duration{
	5 * time.Second,
	time.Minute,
	5 * time.Minute,
	15 * time.Minute,
	30 * time.Minute,
}

type waVersionFetcher func(ctx context.Context) (store.WAVersionContainer, error)

type waVersionResolver struct {
	// fetchMu serializes lookups; mu guards the state and is never held during
	// network access, so handshakes reading current() never wait for a lookup.
	fetchMu   sync.Mutex
	mu        sync.Mutex
	compiled  store.WAVersionContainer
	override  store.WAVersionContainer
	latest    store.WAVersionContainer
	checkedAt time.Time
	fetch     waVersionFetcher
	now       func() time.Time
}

func newWAVersionResolver(compiled, override store.WAVersionContainer, fetch waVersionFetcher) *waVersionResolver {
	return &waVersionResolver{compiled: compiled, override: override, fetch: fetch, now: time.Now}
}

// waVersionOverride reads WHATSAPP_VERSION_MAJOR/MINOR/PATCH. All three must be
// set; the override then wins over WhatsApp Web and is never refreshed.
func waVersionOverride(major, minor, patch int) store.WAVersionContainer {
	if major <= 0 || minor <= 0 || patch <= 0 {
		return store.WAVersionContainer{}
	}
	return store.WAVersionContainer{uint32(major), uint32(minor), uint32(patch)}
}

func waVersionLess(a, b store.WAVersionContainer) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

func newestWAVersion(a, b store.WAVersionContainer) store.WAVersionContainer {
	if waVersionLess(a, b) {
		return b
	}
	return a
}

// current returns the version for the next handshake without network access.
func (r *waVersionResolver) current() store.WAVersionContainer {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.currentLocked()
}

func (r *waVersionResolver) currentLocked() store.WAVersionContainer {
	if !r.override.IsZero() {
		return r.override
	}
	return newestWAVersion(r.compiled, r.latest)
}

// refresh asks WhatsApp Web for its revision when the last lookup is older
// than waVersionRefreshInterval, or always when force is set (after a 405).
// A failed lookup keeps the previous version and is reported to the caller.
// The result never goes below the compiled or a previously seen revision.
func (r *waVersionResolver) refresh(ctx context.Context, force bool) (store.WAVersionContainer, error) {
	r.fetchMu.Lock()
	defer r.fetchMu.Unlock()

	r.mu.Lock()
	if !r.override.IsZero() || (!force && !r.checkedAt.IsZero() && r.now().Sub(r.checkedAt) < waVersionRefreshInterval) {
		version := r.currentLocked()
		r.mu.Unlock()
		return version, nil
	}
	r.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, waVersionFetchTimeout)
	defer cancel()
	fetched, err := r.fetch(ctx)

	r.mu.Lock()
	defer r.mu.Unlock()
	// Failures also count as a check: a non-forced caller (every StartClient)
	// must not hammer WhatsApp Web while it is unreachable.
	r.checkedAt = r.now()
	if err != nil {
		return r.currentLocked(), err
	}
	if fetched.IsZero() {
		return r.currentLocked(), errors.New("WhatsApp Web returned an empty version")
	}
	r.latest = newestWAVersion(r.latest, fetched)
	return r.currentLocked(), nil
}

// applyWAVersion stamps the revision on a handshake payload. BuildHash only
// exists on registration (QR/pairing) payloads.
func applyWAVersion(payload *waWa6.ClientPayload, version store.WAVersionContainer) {
	if payload == nil {
		return
	}
	if payload.UserAgent != nil {
		payload.UserAgent.AppVersion = version.ProtoAppVersion()
	}
	if payload.DevicePairingData != nil {
		hash := version.Hash()
		payload.DevicePairingData.BuildHash = hash[:]
	}
}

// clientPayload builds the hook for Client.GetClientPayload. It is evaluated on
// every handshake, including whatsmeow's own automatic reconnects. used keeps
// the revision this client sent, which is the one a later 405 rejected.
func (r *waVersionResolver) clientPayload(client *whatsmeow.Client, used *atomic.Pointer[store.WAVersionContainer]) func() *waWa6.ClientPayload {
	return func() *waWa6.ClientPayload {
		payload := client.Store.GetClientPayload()
		version := r.current()
		applyWAVersion(payload, version)
		used.Store(&version)
		return payload
	}
}

// handshakeVersion is the revision of this client's last handshake.
func (mycli *MyClient) handshakeVersion() store.WAVersionContainer {
	if used := mycli.usedWAVersion.Load(); used != nil {
		return *used
	}
	if mycli.waVersions != nil {
		return mycli.waVersions.current()
	}
	return store.GetWAVersion()
}

// fetchLatestWAVersion uses whatsmeow's lookup of web.whatsapp.com and falls
// back to the service worker parser that this provider already had.
func fetchLatestWAVersion(ctx context.Context) (store.WAVersionContainer, error) {
	latest, err := whatsmeow.GetLatestVersion(ctx, nil)
	if err == nil && latest != nil {
		return *latest, nil
	}
	legacy, legacyErr := fetchWhatsAppWebVersion(ctx)
	if legacyErr != nil {
		return store.WAVersionContainer{}, fmt.Errorf("web.whatsapp.com: %v; sw.js: %w", err, legacyErr)
	}
	return store.WAVersionContainer{uint32(legacy.Major), uint32(legacy.Minor), uint32(legacy.Patch)}, nil
}

// outdatedClient is the part of *whatsmeow.Client used by the recovery loop.
type outdatedClient interface {
	IsLoggedIn() bool
	Connect() error
}

type outdatedRecovery struct {
	versions *waVersionResolver
	client   outdatedClient
	done     <-chan struct{}
	delays   []time.Duration
	settle   time.Duration // wait for the last handshake before giving up
	logInfo  func(format string, args ...any)
	logError func(format string, args ...any)
}

// run reconnects the same client, keeping its authenticated session, as soon as
// WhatsApp Web publishes a revision newer than the rejected one. It returns
// true once the client is logged in again, and false when the generation ends
// or the attempts run out (the image must then be updated, or
// WHATSAPP_VERSION_* set).
func (o outdatedRecovery) run(rejected store.WAVersionContainer) bool {
	attempted := rejected
	for _, delay := range o.delays {
		select {
		case <-o.done:
			return false
		case <-time.After(delay):
		}
		if o.client.IsLoggedIn() {
			return true
		}
		next, err := o.versions.refresh(context.Background(), true)
		if err != nil {
			o.logError("Could not check the WhatsApp Web version: %v", err)
		}
		if !waVersionLess(attempted, next) {
			o.logError("WhatsApp still rejects %s and no newer WhatsApp Web version is available yet", attempted.String())
			continue
		}
		attempted = next
		o.logInfo("Reconnecting with WhatsApp Web version %s after 405 for %s", next.String(), rejected.String())
		if err := o.client.Connect(); err != nil && !errors.Is(err, whatsmeow.ErrAlreadyConnected) {
			o.logError("Reconnect with version %s failed: %v", next.String(), err)
		}
	}
	select {
	case <-o.done:
		return false
	case <-time.After(o.settle):
	}
	return o.client.IsLoggedIn()
}

// recoverOutdatedClient starts at most one recovery per client generation.
func (mycli *MyClient) recoverOutdatedClient(rejected store.WAVersionContainer) {
	// Only authenticated sessions are resumed; a QR attempt is restarted by
	// whoever asked for it, never in the background.
	if mycli.waVersions == nil || mycli.WAClient == nil || mycli.WAClient.Store == nil || mycli.WAClient.Store.ID == nil {
		return
	}
	if !mycli.outdatedRecovering.CompareAndSwap(false, true) {
		return
	}
	logger := mycli.loggerWrapper.GetLogger(mycli.userID)
	recovery := outdatedRecovery{
		versions: mycli.waVersions,
		client:   mycli.WAClient,
		done:     mycli.doneChannel,
		delays:   outdatedRecoveryDelays,
		settle:   30 * time.Second,
		logInfo: func(format string, args ...any) {
			logger.LogInfo("[%s] "+format, append([]any{mycli.userID}, args...)...)
		},
		logError: func(format string, args ...any) {
			logger.LogError("[%s] "+format, append([]any{mycli.userID}, args...)...)
		},
	}
	go func() {
		defer mycli.outdatedRecovering.Store(false)
		if !recovery.run(rejected) {
			recovery.logError("Client still outdated (405). Update the image or set WHATSAPP_VERSION_MAJOR/MINOR/PATCH, then reconnect the instance")
		}
	}()
}

// outdatedWebhookData is sent as a ConnectFailure so receivers that already
// handle connection failures see the cause instead of a silent disconnect.
func outdatedWebhookData(rejected store.WAVersionContainer) map[string]interface{} {
	return map[string]interface{}{
		"Reason":        405,
		"reason":        "405: client outdated",
		"Message":       "client outdated",
		"clientVersion": rejected.String(),
	}
}

var _ outdatedClient = (*whatsmeow.Client)(nil)
