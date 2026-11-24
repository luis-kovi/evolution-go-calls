package whatsmeow_service

import (
	"errors"
	"testing"

	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
)

const pairedJID = "5511999990000:71@s.whatsapp.net"

func TestAutomaticDisconnectReasons(t *testing.T) {
	automatic := []string{
		disconnectReasonSocketClosed,
		disconnectReasonReconnecting,
		"405: client outdated (2.3000.1043636855)",
	}
	for _, reason := range automatic {
		if !isAutomaticDisconnectReason(reason) {
			t.Errorf("%q should be automatic", reason)
		}
	}
	manual := []string{
		"",
		disconnectReasonStopped,
		"401: logged out from another device",
		"Maximum QR code count (5) reached",
		"403: temporary ban",
	}
	for _, reason := range manual {
		if isAutomaticDisconnectReason(reason) {
			t.Errorf("%q must not be resumed on startup", reason)
		}
	}
}

func TestResumeOnStartup(t *testing.T) {
	cases := []struct {
		name     string
		instance *instance_model.Instance
		want     bool
	}{
		{"marked connected", &instance_model.Instance{Connected: true}, true},
		{"405 with paired session", &instance_model.Instance{Jid: pairedJID, DisconnectReason: "405: client outdated (2.3000.1043636855)"}, true},
		{"socket closed with paired session", &instance_model.Instance{Jid: pairedJID, DisconnectReason: disconnectReasonSocketClosed}, true},
		{"forced reconnect interrupted", &instance_model.Instance{Jid: pairedJID, DisconnectReason: disconnectReasonReconnecting}, true},
		{"stopped on request", &instance_model.Instance{Jid: pairedJID, DisconnectReason: disconnectReasonStopped}, false},
		{"logged out", &instance_model.Instance{Jid: pairedJID, DisconnectReason: "401: logged out from another device"}, false},
		{"never paired", &instance_model.Instance{DisconnectReason: disconnectReasonSocketClosed}, false},
		{"QR limit", &instance_model.Instance{DisconnectReason: "Maximum QR code count (5) reached"}, false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		if got := resumeOnStartup(tc.instance); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestStoppedReasonMarksRequestedStops(t *testing.T) {
	cases := []struct {
		instance *instance_model.Instance
		want     string
	}{
		{&instance_model.Instance{Jid: pairedJID, DisconnectReason: ""}, disconnectReasonStopped},
		{&instance_model.Instance{Jid: pairedJID, DisconnectReason: disconnectReasonSocketClosed}, disconnectReasonStopped},
		{&instance_model.Instance{Jid: pairedJID, DisconnectReason: "405: client outdated (x)"}, disconnectReasonStopped},
		{&instance_model.Instance{Jid: pairedJID, DisconnectReason: "401: logged out from another device"}, "401: logged out from another device"},
		{&instance_model.Instance{DisconnectReason: "Maximum QR code count (5) reached"}, "Maximum QR code count (5) reached"},
		{&instance_model.Instance{DisconnectReason: ""}, ""},
	}
	for _, tc := range cases {
		if got := stoppedReason(tc.instance); got != tc.want {
			t.Errorf("reason %q jid %q: got %q, want %q", tc.instance.DisconnectReason, tc.instance.Jid, got, tc.want)
		}
	}
	// After a requested stop, startup must not resume the session.
	stopped := &instance_model.Instance{Jid: pairedJID, DisconnectReason: disconnectReasonSocketClosed}
	stopped.DisconnectReason = stoppedReason(stopped)
	if resumeOnStartup(stopped) {
		t.Fatal("a requested stop was resumed on startup")
	}
}

func TestSelectStartupInstancesRequiresStoredSession(t *testing.T) {
	connected := &instance_model.Instance{Id: "connected", Connected: true, Jid: "1@s.whatsapp.net"}
	outdated := &instance_model.Instance{Id: "outdated", Jid: "2@s.whatsapp.net", DisconnectReason: "405: client outdated (x)"}
	revoked := &instance_model.Instance{Id: "revoked", Jid: "3@s.whatsapp.net", DisconnectReason: disconnectReasonSocketClosed}
	broken := &instance_model.Instance{Id: "broken", Jid: "4@s.whatsapp.net", DisconnectReason: disconnectReasonReconnecting}
	stopped := &instance_model.Instance{Id: "stopped", Jid: "5@s.whatsapp.net", DisconnectReason: disconnectReasonStopped}

	stored := map[string]bool{"2@s.whatsapp.net": true, "5@s.whatsapp.net": true}
	var checked []string
	hasSession := func(jid string) (bool, error) {
		checked = append(checked, jid)
		if jid == "4@s.whatsapp.net" {
			return false, errors.New("db down")
		}
		return stored[jid], nil
	}
	skipped := map[string]string{}
	logSkip := func(instance *instance_model.Instance, reason string) { skipped[instance.Id] = reason }

	got := selectStartupInstances([]*instance_model.Instance{connected, outdated, revoked, broken, stopped}, hasSession, logSkip)

	ids := make([]string, 0, len(got))
	for _, instance := range got {
		ids = append(ids, instance.Id)
	}
	if len(ids) != 2 || ids[0] != "connected" || ids[1] != "outdated" {
		t.Fatalf("selected %v, want [connected outdated]", ids)
	}
	// Connected rows keep the previous behaviour: no session lookup.
	for _, jid := range checked {
		if jid == connected.Jid || jid == stopped.Jid {
			t.Fatalf("unexpected session lookup for %s", jid)
		}
	}
	if skipped["revoked"] == "" || skipped["broken"] == "" {
		t.Fatalf("skips not logged: %v", skipped)
	}
	if _, ok := skipped["stopped"]; ok {
		t.Fatal("a requested stop is not a candidate and needs no log")
	}
}
