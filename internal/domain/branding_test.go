package domain

import (
	"strings"
	"testing"
)

func TestServiceIdentityAndLoginMessageUseTelesrvBrand(t *testing.T) {
	serviceUser := OfficialSystemUser()
	if serviceUser.FirstName != "ShuzaGram" || serviceUser.Username != "shuzagram" {
		t.Fatalf("service user = %+v, want ShuzaGram identity", serviceUser)
	}
	message, err := OfficialLoginCodeMessage(42, "", "12345", 1)
	if err != nil {
		t.Fatalf("build login message: %v", err)
	}
	if !strings.Contains(message.Body, "ShuzaGram") || strings.Contains(strings.ToLower(message.Body), "telegram") {
		t.Fatalf("login message exposes wrong brand: %q", message.Body)
	}
}

func TestLoginCodeTemplateSnapshotAndEntityOffset(t *testing.T) {
	template := SnapshotLoginCodeMessageTemplate("🔐 {{code}} for {{server_name}}")
	if strings.Contains(template, "{{server_name}}") || strings.Count(template, "{{code}}") != 1 {
		t.Fatalf("template snapshot = %q", template)
	}
	message, err := OfficialLoginCodeMessageWithTemplate(42, template, "12345", 1)
	if err != nil || !strings.Contains(message.Body, "🔐 12345 for ShuzaGram") || len(message.Entities) != 1 || message.Entities[0].Offset != 3 || message.Entities[0].Length != 5 {
		t.Fatalf("rendered message = %+v err=%v", message, err)
	}
	if got := SnapshotLoginCodeMessageTemplate("missing code"); !strings.Contains(got, "{{code}}") || strings.Contains(got, "missing code") {
		t.Fatalf("invalid template fallback = %q", got)
	}
}
