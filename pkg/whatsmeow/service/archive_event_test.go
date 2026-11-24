package whatsmeow_service

import (
	"encoding/json"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func TestArchiveWebhookDataFromRawEvent(t *testing.T) {
	ts := time.Date(2026, 11, 24, 2, 31, 0, 0, time.UTC)
	evt := &events.Archive{
		JID:          types.NewJID("5511999990000", types.DefaultUserServer),
		Timestamp:    ts,
		Action:       &waSyncAction.ArchiveChatAction{Archived: proto.Bool(true)},
		FromFullSync: true,
	}

	// The handler starts with postMap["data"] = rawEvt; the old code asserted
	// that value to map[string]interface{} and panicked.
	var raw interface{} = evt
	if _, ok := raw.(map[string]interface{}); ok {
		t.Fatal("raw event unexpectedly is a map")
	}

	data := archiveWebhookData(evt)
	if data["JID"] != evt.JID || data["Timestamp"] != ts || data["FromFullSync"] != true {
		t.Fatalf("unexpected payload: %#v", data)
	}

	encoded, err := json.Marshal(map[string]interface{}{"event": "Archive", "data": data})
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Data struct {
			JID          string `json:"JID"`
			FromFullSync bool   `json:"FromFullSync"`
			Action       struct {
				Archived bool `json:"archived"`
			} `json:"Action"`
		} `json:"data"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Data.JID != "5511999990000@s.whatsapp.net" || !decoded.Data.FromFullSync || !decoded.Data.Action.Archived {
		t.Fatalf("webhook JSON lost fields: %s", encoded)
	}
}

func TestArchiveWebhookDataWithoutAction(t *testing.T) {
	data := archiveWebhookData(&events.Archive{JID: types.NewJID("123", types.GroupServer)})
	if _, err := json.Marshal(data); err != nil {
		t.Fatalf("nil action must still serialize: %v", err)
	}
}
