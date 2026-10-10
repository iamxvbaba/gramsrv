package bots

import (
	"testing"

	"telesrv/internal/domain"
)

func TestParseGramsrvCommand(t *testing.T) {
	tests := []struct {
		body, command, argument string
	}{
		{body: "/account @testing", command: "account", argument: "@testing"},
		{body: "/freeze@gramsrv 1780243437", command: "freeze", argument: "1780243437"},
		{body: "hello", command: "", argument: ""},
	}
	for _, tt := range tests {
		command, argument := parseGramsrvCommand(tt.body)
		if command != tt.command || argument != tt.argument {
			t.Fatalf("parseGramsrvCommand(%q) = (%q, %q), want (%q, %q)", tt.body, command, argument, tt.command, tt.argument)
		}
	}
}

func TestParseGramsrvCallback(t *testing.T) {
	targetID, frozen, ok := parseGramsrvCallback([]byte("gs:account:1780243437:1"))
	if !ok || targetID != 1780243437 || !frozen {
		t.Fatalf("valid freeze callback = (%d, %t, %t)", targetID, frozen, ok)
	}
	for _, data := range [][]byte{
		[]byte("gs:account:1780243437:2"),
		[]byte("gs:account:0:1"),
		[]byte("other:account:1780243437:1"),
	} {
		if _, _, ok := parseGramsrvCallback(data); ok {
			t.Fatalf("parseGramsrvCallback(%q) accepted invalid data", data)
		}
	}
}

func TestGramsrvChatAllowlistIsNumeric(t *testing.T) {
	svc := &Service{gramsrvAdminChatIDs: map[int64]struct{}{1780243437: {}}}
	if !svc.gramsrvChatAllowed(1780243437) {
		t.Fatal("configured chat_id was rejected")
	}
	if svc.gramsrvChatAllowed(1780243438) || svc.gramsrvChatAllowed(0) {
		t.Fatal("unconfigured chat_id was accepted")
	}
	if domain.GramsrvBotUserID == 0 {
		t.Fatal("gramsrv bot identity is not configured")
	}
}
