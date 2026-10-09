package whatsmeow_service

import (
	"context"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// Decrypt edits before transport aliases are rewritten. The message secret is
// scoped to the original chat and sender, so the original LID must be retained.
func normalizeSecretEdit(ctx context.Context, client *whatsmeow.Client, event *events.Message) error {
	if event == nil || event.Message.GetSecretEncryptedMessage() == nil {
		return nil
	}
	secret := event.Message.GetSecretEncryptedMessage()
	if secret.GetSecretEncType() != waE2E.SecretEncryptedMessage_MESSAGE_EDIT {
		return nil
	}
	decrypted, err := client.DecryptSecretEncryptedMessage(ctx, event)
	if err != nil {
		return err
	}
	kind := waE2E.ProtocolMessage_MESSAGE_EDIT
	event.Message = &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type: &kind, Key: secret.GetTargetMessageKey(), EditedMessage: decrypted,
	}}
	// The protocol contains the original target ID; this event's ID belongs to
	// the edit operation and must not be interpreted as the message being edited.
	event.IsEdit = false
	return nil
}

func normalizeHistorySecretEdits(ctx context.Context, client *whatsmeow.Client, history *events.HistorySync) (int, int) {
	recovered, unavailable := 0, 0
	for _, conversation := range history.Data.GetConversations() {
		chat, err := types.ParseJID(conversation.GetID())
		if err != nil {
			continue
		}
		for _, item := range conversation.GetMessages() {
			web := item.GetMessage()
			if web.GetMessage().GetSecretEncryptedMessage() == nil {
				continue
			}
			event, err := client.ParseWebMessage(chat, web)
			if err != nil || event == nil {
				unavailable++
				continue
			}
			if event.Message.GetSecretEncryptedMessage().GetSecretEncType() != waE2E.SecretEncryptedMessage_MESSAGE_EDIT {
				continue
			}
			if err = normalizeSecretEdit(ctx, client, event); err != nil {
				unavailable++
				continue
			}
			web.Message = event.Message
			recovered++
		}
	}
	return recovered, unavailable
}
