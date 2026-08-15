package mail

import (
	"context"
	"strings"
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

func TestMissingSortCursorRequiresFullCandidateScan(t *testing.T) {
	uids := []imap.UID{5, 4, 2, 1}
	if !missingSortCursor(uids, 3) || missingSortCursor(uids, 4) || missingSortCursor(uids, 0) {
		t.Fatal("missing SORT cursor detection did not distinguish deleted and present cursors")
	}
}

func TestIMAPSearchUsesDaySupersetForExactTimestampBounds(t *testing.T) {
	since := time.Date(2026, 8, 15, 12, 30, 0, 0, time.UTC)
	before := time.Date(2026, 8, 16, 8, 15, 0, 0, time.UTC)
	gotSince, gotBefore := imapSearchDateBounds(since, before)
	if want := time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC); !gotSince.Equal(want) {
		t.Fatalf("IMAP since = %v, want %v", gotSince, want)
	}
	if want := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC); !gotBefore.Equal(want) {
		t.Fatalf("IMAP before = %v, want %v", gotBefore, want)
	}
	if window := orderedUIDWindow([]imap.UID{5, 4, 3, 2, 1}, 0, 0); len(window) != 5 {
		t.Fatalf("exact-bound SORT window was prematurely truncated: %v", window)
	}
}

func TestIMAPSearchBoundsCoverExtremeRFC3339Offsets(t *testing.T) {
	plusFourteen := time.FixedZone("+14", 14*60*60)
	minusTwelve := time.FixedZone("-12", -12*60*60)
	since := time.Date(2026, 8, 15, 0, 0, 0, 0, plusFourteen)
	before := time.Date(2026, 8, 15, 0, 0, 0, 0, minusTwelve)
	gotSince, gotBefore := imapSearchDateBounds(since, before)
	if gotSince.After(since.UTC()) || !gotBefore.After(before.UTC()) {
		t.Fatalf("IMAP bounds %v..%v do not contain exact instants %v..%v", gotSince, gotBefore, since.UTC(), before.UTC())
	}
}

func TestSortWindowIsUnboundedForEveryBeforeFilter(t *testing.T) {
	midnight := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)
	if !needsUnboundedSortWindow(SearchOptions{Before: midnight}) {
		t.Fatal("midnight BEFORE filter allowed widened provider results to truncate the SORT window")
	}
	if needsUnboundedSortWindow(SearchOptions{}) {
		t.Fatal("unbounded query disabled safe SORT windowing")
	}
}

func TestDialIMAPContextHonorsPreCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	_, err := dialIMAPContext(ctx, model.IMAPConfig{Address: "203.0.113.1:993", TLS: true})
	if err == nil || time.Since(started) > time.Second {
		t.Fatalf("pre-canceled dial returned %v after %v", err, time.Since(started))
	}
}

func TestDialIMAPRejectsRemoteCleartextEvenWhenInsecure(t *testing.T) {
	_, err := dialIMAPContext(context.Background(), model.IMAPConfig{Address: "imap.example.test:143", Insecure: true})
	if err == nil || !strings.Contains(err.Error(), "remote cleartext IMAP") {
		t.Fatalf("remote cleartext dial returned %v", err)
	}
}

func TestSafePreviewSizeRequiresPositiveBoundedProviderSize(t *testing.T) {
	if safePreviewSize(0, 64<<10) || safePreviewSize(-1, 64<<10) || safePreviewSize((64<<10)+1, 64<<10) {
		t.Fatal("unsafe or missing message size allowed preview fetch")
	}
	if !safePreviewSize(1, 64<<10) || !safePreviewSize(64<<10, 64<<10) {
		t.Fatal("bounded positive message size rejected preview fetch")
	}
}

func TestMessageOrderingMatchesSortTieBreaker(t *testing.T) {
	when := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	if !messageBefore(model.Message{UID: 1, ReceivedAt: when}, model.Message{UID: 2, ReceivedAt: when}) {
		t.Fatal("message ordering does not match IMAP SORT's implicit mailbox-order tie-breaker")
	}
}
