package whatsmeow_service

import "go.mau.fi/whatsmeow/types/events"

// archiveWebhookData is the webhook payload of an Archive event (a chat was
// archived or unarchived, live or during a full app state sync).
func archiveWebhookData(evt *events.Archive) map[string]interface{} {
	return map[string]interface{}{
		"JID":          evt.JID,
		"Timestamp":    evt.Timestamp,
		"Action":       evt.Action,
		"FromFullSync": evt.FromFullSync,
	}
}
