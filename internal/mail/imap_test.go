package mail

import (
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
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

func TestOrderedUIDWindowBoundsContinuationFetch(t *testing.T) {
	uids := make([]imap.UID, 1000)
	for index := range uids {
		uids[index] = imap.UID(1000 - index)
	}
	window := orderedUIDWindow(uids, 500, 26)
	if len(window) != 26 || window[0] != 499 || window[25] != 474 {
		t.Fatalf("ordered UID window = %#v", window)
	}
	first := orderedUIDWindow(uids, 0, 26)
	if len(first) != 26 || first[0] != 1000 || first[25] != 975 {
		t.Fatalf("first ordered UID window = %#v", first)
	}
}

func TestIMAPSearchUsesDaySupersetForExactTimestampBounds(t *testing.T) {
	since := time.Date(2026, 8, 15, 12, 30, 0, 0, time.UTC)
	before := time.Date(2026, 8, 16, 8, 15, 0, 0, time.UTC)
	gotSince, gotBefore := imapSearchDateBounds(since, before)
	if want := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC); !gotSince.Equal(want) {
		t.Fatalf("IMAP since = %v, want %v", gotSince, want)
	}
	if want := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC); !gotBefore.Equal(want) {
		t.Fatalf("IMAP before = %v, want %v", gotBefore, want)
	}
	if window := orderedUIDWindow([]imap.UID{5, 4, 3, 2, 1}, 0, 0); len(window) != 5 {
		t.Fatalf("exact-bound SORT window was prematurely truncated: %v", window)
	}
}

func TestMessageOrderingMatchesSortTieBreaker(t *testing.T) {
	when := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	if !messageBefore(model.Message{UID: 1, ReceivedAt: when}, model.Message{UID: 2, ReceivedAt: when}) {
		t.Fatal("message ordering does not match IMAP SORT's implicit mailbox-order tie-breaker")
	}
}
