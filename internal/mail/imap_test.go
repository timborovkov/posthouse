package mail

import (
	"testing"
	"time"

	"github.com/timborovkov/posthouse/internal/model"
)

func TestPreviewCollapsesWhitespaceAndLimitsLength(t *testing.T) {
	input := []byte("hello\r\n\tworld   " + string(make([]byte, 500)))
	got := preview(input)
	if len([]rune(got)) > 401 {
		t.Fatalf("preview length is %d", len([]rune(got)))
	}
	if got[:11] != "hello world" {
		t.Fatalf("preview returned %q", got)
	}
}

func TestMessagePreviewSkipsHeaders(t *testing.T) {
	got := messagePreview([]byte("Subject: private\r\nContent-Type: text/plain\r\n\r\nHello from the body"))
	if got != "Hello from the body" {
		t.Fatalf("messagePreview returned %q", got)
	}
}

func TestMessageOrderingUsesProviderDateBeforeUID(t *testing.T) {
	newer := model.Message{UID: 2, ReceivedAt: time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)}
	older := model.Message{UID: 9, ReceivedAt: time.Date(2026, 8, 15, 11, 0, 0, 0, time.UTC)}
	if !messageBefore(newer, older) || messageBefore(older, newer) {
		t.Fatalf("message ordering followed UID instead of provider date")
	}
}
