package chat_service

import (
	"context"
	"errors"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
)

type historySyncClient interface {
	BuildHistorySyncRequest(*types.MessageInfo, int) *waE2E.Message
	SendPeerMessage(context.Context, *waE2E.Message) (whatsmeow.SendResponse, error)
}

func sendHistorySyncRequest(ctx context.Context, client historySyncClient, data *HistorySyncRequestStruct) (*whatsmeow.SendResponse, error) {
	if data == nil || data.MessageInfo == nil || data.MessageInfo.Chat.IsEmpty() {
		return nil, errors.New("messageInfo.Chat is required")
	}
	info := *data.MessageInfo
	if info.Timestamp.IsZero() {
		info.Timestamp = time.Now()
	}
	request := client.BuildHistorySyncRequest(&info, data.Count)
	// The chat belongs inside the history request. The envelope must go to our
	// primary device: SendMessage(group, ..., Peer:true) still takes the group
	// send path in whatsmeow and never requests the primary device's history.
	response, err := client.SendPeerMessage(ctx, request)
	if err != nil {
		return nil, err
	}
	return &response, nil
}
