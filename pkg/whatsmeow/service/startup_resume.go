package whatsmeow_service

import (
	"context"
	"fmt"
	"strings"

	"go.mau.fi/whatsmeow/store/sqlstore"

	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	"github.com/evolution-foundation/evolution-go/pkg/utils"
)

// `connected` mixes socket state with intent: automatic drops (a 405, the
// server closing the websocket, a forced reconnect) clear it, so startup used to
// skip those instances until some API request started them on demand. Startup
// now also resumes sessions that dropped on their own. A manual disconnect,
// logout or QR attempt is never resumed.
const (
	disconnectReasonSocketClosed   = "Disconnected emitted because the websocket is closed by the server."
	disconnectReasonReconnecting   = "Reconnecting"
	disconnectReasonOutdatedPrefix = "405: client outdated"
	// Written when a paired session's generation is stopped on request, so a
	// stale automatic reason cannot make startup resume it.
	disconnectReasonStopped = "Stopped by request"
)

// isAutomaticDisconnectReason reports reasons written by the provider itself
// when a paired session dropped without anyone asking for it.
func isAutomaticDisconnectReason(reason string) bool {
	return reason == disconnectReasonSocketClosed ||
		reason == disconnectReasonReconnecting ||
		strings.HasPrefix(reason, disconnectReasonOutdatedPrefix)
}

// resumeOnStartup is the first filter: instances marked connected, as before,
// plus paired instances whose last disconnect was automatic.
func resumeOnStartup(instance *instance_model.Instance) bool {
	if instance == nil {
		return false
	}
	if instance.Connected {
		return true
	}
	return instance.Jid != "" && isAutomaticDisconnectReason(instance.DisconnectReason)
}

// stoppedReason is the reason persisted when a generation is stopped on
// request. Paired sessions with no reason or an automatic one get the explicit
// stop marker; anything else (logout, QR limits) is kept.
func stoppedReason(instance *instance_model.Instance) string {
	if instance.Jid != "" && (instance.DisconnectReason == "" || isAutomaticDisconnectReason(instance.DisconnectReason)) {
		return disconnectReasonStopped
	}
	return instance.DisconnectReason
}

// selectStartupInstances keeps the instances to start. An instance that is not
// marked connected must still have its device in the whatsmeow store: without
// it StartClient would create a new device and show a QR code nobody asked for.
func selectStartupInstances(
	instances []*instance_model.Instance,
	hasSession func(jid string) (bool, error),
	logSkip func(instance *instance_model.Instance, reason string),
) []*instance_model.Instance {
	selected := make([]*instance_model.Instance, 0, len(instances))
	for _, instance := range instances {
		if !resumeOnStartup(instance) {
			continue
		}
		if !instance.Connected {
			ok, err := hasSession(instance.Jid)
			if err != nil {
				logSkip(instance, fmt.Sprintf("could not read the stored session: %v", err))
				continue
			}
			if !ok {
				logSkip(instance, "no stored session for its JID")
				continue
			}
		}
		selected = append(selected, instance)
	}
	return selected
}

// hasStoredSession reports whether the whatsmeow store still holds the device
// for jid. A LoggedOut deletes it, so this is the authoritative check.
func (w whatsmeowService) hasStoredSession(ctx context.Context, rawJID string) (bool, error) {
	jid, ok := utils.ParseJID(rawJID)
	if !ok {
		return false, nil
	}
	var container *sqlstore.Container
	var err error
	if w.config.PostgresAuthDB != "" {
		container, err = w.sessionStore.get(ctx)
	} else {
		dsn := fmt.Sprintf("file:%s/dbdata/main.db?_pragma=foreign_keys(1)&_busy_timeout=5000&cache=shared&mode=rwc&_journal_mode=WAL", w.exPath)
		container, err = sqlstore.New(ctx, "sqlite", dsn, nil)
		if err == nil {
			defer container.Close()
		}
	}
	if err != nil {
		return false, err
	}
	device, err := container.GetDevice(ctx, jid)
	if err != nil {
		return false, err
	}
	return device != nil, nil
}
