package send_service

import (
	"encoding/json"
	"testing"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

func TestReplyButtonsUseNativeFlow(t *testing.T) {
	data := &ButtonStruct{Title: "Teste", Description: "Escolha uma opção", Footer: "Rekovi"}
	buttons := []*waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{
		{Name: proto.String("quick_reply"), ButtonParamsJSON: proto.String(`{"display_text":"Aceitar","id":"aceitar_1"}`)},
		{Name: proto.String("quick_reply"), ButtonParamsJSON: proto.String(`{"display_text":"Recusar","id":"recusar_2"}`)},
	}
	secret := make([]byte, 32)
	msg := replyButtonMessage(data, &waE2E.InteractiveMessage_Header{
		Title: proto.String(data.Title), HasMediaAttachment: proto.Bool(false),
	}, buttons, secret, `{"from":"api","templateId":123}`)
	wire, err := proto.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	decoded := &waE2E.Message{}
	if err := proto.Unmarshal(wire, decoded); err != nil {
		t.Fatal(err)
	}
	inner := decoded
	if inner == nil || inner.GetButtonsMessage() != nil || inner.GetDocumentWithCaptionMessage() != nil {
		t.Fatal("legacy or missing interactive envelope")
	}
	interactive := inner.GetInteractiveMessage()
	if interactive.GetHeader().GetTitle() != data.Title || interactive.GetBody().GetText() != data.Description || interactive.GetFooter().GetText() != data.Footer {
		t.Fatal("message text changed")
	}
	flow := interactive.GetNativeFlowMessage()
	if len(flow.GetButtons()) != 2 || flow.GetMessageVersion() != 1 {
		t.Fatal("native flow buttons missing")
	}
	for i, button := range flow.GetButtons() {
		var value map[string]string
		if button.GetName() != "quick_reply" {
			t.Fatal("wrong interactive action")
		}
		if err := json.Unmarshal([]byte(button.GetButtonParamsJSON()), &value); err != nil {
			t.Fatal(err)
		}
		if value["id"] != []string{"aceitar_1", "recusar_2"}[i] {
			t.Fatal("callback identity changed")
		}
	}
	if interactive.ContextInfo == nil || len(decoded.GetMessageContextInfo().GetMessageSecret()) != 32 || decoded.GetMessageContextInfo().GetDeviceListMetadataVersion() != 2 {
		t.Fatal("mobile context missing")
	}
}

func TestReplyButtonIDSurvivesRequestDecoding(t *testing.T) {
	var data ButtonStruct
	if err := json.Unmarshal([]byte(`{"id":"stable-provider-id","number":"5511985120125"}`), &data); err != nil {
		t.Fatal(err)
	}
	if data.Id != "stable-provider-id" {
		t.Fatal("provider ID discarded")
	}
}
