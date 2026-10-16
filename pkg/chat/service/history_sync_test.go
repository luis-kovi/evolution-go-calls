package chat_service

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
)

type historyPeerStub struct {
	message *waE2E.Message
	err     error
}

func (s *historyPeerStub) BuildHistorySyncRequest(info *types.MessageInfo, count int) *waE2E.Message {
	return (&whatsmeow.Client{}).BuildHistorySyncRequest(info, count)
}

func (s *historyPeerStub) SendPeerMessage(_ context.Context, message *waE2E.Message) (whatsmeow.SendResponse, error) {
	s.message = message
	return whatsmeow.SendResponse{ID: "peer-request-id"}, s.err
}

func TestHistorySyncRequestsPrimaryDeviceAndPreservesChatCursor(t *testing.T) {
	for _, server := range []string{types.GroupServer, types.DefaultUserServer} {
		t.Run(server, func(t *testing.T) {
			client := &historyPeerStub{}
			info := &types.MessageInfo{
				MessageSource: types.MessageSource{Chat: types.NewJID("120363123456789", server), IsFromMe: true},
				ID:            "last-known-message",
				Timestamp:     time.Unix(1792128000, 0),
			}
			response, err := sendHistorySyncRequest(context.Background(), client, &HistorySyncRequestStruct{MessageInfo: info, Count: 50})
			if err != nil || response.ID != "peer-request-id" || client.message == nil {
				t.Fatalf("peer request failed: response=%v error=%v", response, err)
			}
			request := client.message.GetProtocolMessage().GetPeerDataOperationRequestMessage().GetHistorySyncOnDemandRequest()
			if request.GetChatJID() != info.Chat.String() || request.GetOldestMsgID() != info.ID ||
				!request.GetOldestMsgFromMe() || request.GetOldestMsgTimestampMS() != info.Timestamp.Unix() || request.GetOnDemandMsgCount() != 50 {
				t.Fatalf("history request lost its original chat or pagination cursor: %v", request)
			}
		})
	}
}

func TestHistorySyncRepairsLegacyMissingTimestampWithoutMutatingInput(t *testing.T) {
	client := &historyPeerStub{}
	info := &types.MessageInfo{MessageSource: types.MessageSource{Chat: types.NewJID("120363123456789", types.GroupServer)}}
	before := time.Now().Unix()
	_, err := sendHistorySyncRequest(context.Background(), client, &HistorySyncRequestStruct{MessageInfo: info, Count: 50})
	if err != nil {
		t.Fatal(err)
	}
	request := client.message.GetProtocolMessage().GetPeerDataOperationRequestMessage().GetHistorySyncOnDemandRequest()
	if request.GetOldestMsgTimestampMS() < before || !info.Timestamp.IsZero() {
		t.Fatal("legacy request was sent with a zero timestamp or input was modified")
	}
}

func TestHistorySyncRejectsMissingChatAndPropagatesPeerFailure(t *testing.T) {
	client := &historyPeerStub{}
	for _, input := range []*HistorySyncRequestStruct{nil, {}, {MessageInfo: &types.MessageInfo{}}} {
		if _, err := sendHistorySyncRequest(context.Background(), client, input); err == nil || client.message != nil {
			t.Fatal("invalid request reached the primary device")
		}
	}
	expected := errors.New("primary device unavailable")
	client.err = expected
	_, err := sendHistorySyncRequest(context.Background(), client, &HistorySyncRequestStruct{
		MessageInfo: &types.MessageInfo{MessageSource: types.MessageSource{Chat: types.NewJID("123", types.GroupServer)}}, Count: 50,
	})
	if !errors.Is(err, expected) {
		t.Fatalf("peer failure was lost: %v", err)
	}
}
