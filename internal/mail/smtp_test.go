package mail

import (
	"strings"
	"testing"

	"github.com/timborovkov/posthouse/internal/model"
)

func TestBuildMessageDoesNotExposeBCCAndNormalizesHeaders(t *testing.T) {
	data := string(buildMessage(model.Identity{Name: "Tim", Email: "tim@example.com"}, "tim@example.com", model.SendMessage{
		To: []string{"one@example.com"}, BCC: []string{"hidden@example.com"}, Subject: "Hello\r\nBcc: attacker@example.com", Text: "line one\nline two",
	}))
	if strings.Contains(data, "hidden@example.com") {
		t.Fatal("message body exposed a Bcc recipient")
	}
	if strings.Contains(data, "\r\nBcc: attacker") {
		t.Fatal("message body allowed header injection")
	}
	if !strings.Contains(data, "line one\r\nline two") {
		t.Fatalf("message did not normalize body newlines: %q", data)
	}
}

func TestSMTPHost(t *testing.T) {
	got, err := smtpHost("smtp.example.com:465")
	if err != nil || got != "smtp.example.com" {
		t.Fatalf("smtpHost returned %q, %v", got, err)
	}
}
