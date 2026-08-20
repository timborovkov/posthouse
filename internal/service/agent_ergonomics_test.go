package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	postmail "github.com/timborovkov/posthouse/internal/mail"
	"github.com/timborovkov/posthouse/internal/model"
)

func TestPrepareMailActionJunkUsesDiscoveredFolder(t *testing.T) {
	t.Setenv("POSTHOUSE_CACHE_KEY", base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	connection := mailConnection("work")
	connection.Mail.Folders.Junk = "Junk"
	application := serviceWithConnections(t, connection)
	application.mailSnapshot = func(_ model.Connection, folder string, uid uint32) (postmail.MessagePrecondition, error) {
		if folder != "INBOX" || uid != 9 {
			t.Fatalf("snapshot folder=%q uid=%d", folder, uid)
		}
		return postmail.MessagePrecondition{UIDValidity: 1}, nil
	}
	prepared, err := application.PrepareMailAction(context.Background(), "work", "mail.junk", MailAction{UID: 9})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Preview["destination"] != "Junk" {
		t.Fatalf("destination = %#v", prepared.Preview["destination"])
	}
}

func TestPrepareMailActionJunkRequiresDiscoveredFolder(t *testing.T) {
	t.Setenv("POSTHOUSE_CACHE_KEY", base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	application := serviceWithConnections(t, mailConnection("work"))
	if _, err := application.PrepareMailAction(context.Background(), "work", "mail.junk", MailAction{UID: 1}); err == nil || !strings.Contains(err.Error(), "no discovered destination folder") {
		t.Fatalf("PrepareMailAction junk error = %v", err)
	}
}

func TestPrepareMailActionBatchUIDsAndCap(t *testing.T) {
	t.Setenv("POSTHOUSE_CACHE_KEY", base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	connection := mailConnection("work")
	connection.Mail.Folders.Archive = "Archive"
	application := serviceWithConnections(t, connection)
	seen := map[uint32]bool{}
	application.mailSnapshot = func(_ model.Connection, _ string, uid uint32) (postmail.MessagePrecondition, error) {
		seen[uid] = true
		return postmail.MessagePrecondition{UIDValidity: 7}, nil
	}
	prepared, err := application.PrepareMailAction(context.Background(), "work", "mail.archive", MailAction{UIDs: []uint32{3, 1, 3}})
	if err != nil {
		t.Fatal(err)
	}
	if !seen[3] || !seen[1] || len(seen) != 2 {
		t.Fatalf("snapshot uids = %#v", seen)
	}
	uids, ok := prepared.Preview["uids"].([]uint32)
	if !ok || len(uids) != 2 || uids[0] != 3 || uids[1] != 1 {
		t.Fatalf("preview uids = %#v", prepared.Preview["uids"])
	}

	tooMany := make([]uint32, maxBatchMailUIDs+1)
	for i := range tooMany {
		tooMany[i] = uint32(i + 1)
	}
	if _, err := application.PrepareMailAction(context.Background(), "work", "mail.archive", MailAction{UIDs: tooMany}); err == nil || !strings.Contains(err.Error(), "at most 100") {
		t.Fatalf("batch cap error = %v", err)
	}
}

func TestUnreadCountsReportsResolveFailurePerConnection(t *testing.T) {
	good := mailConnection("work")
	bad := mailConnection("personal")
	bad.Mail.SecretEnv = "MISSING_PERSONAL_PASSWORD"
	bad.Mail.Secret = model.SecretRef{}
	application := serviceWithConnections(t, good, bad)
	summaries, err := application.UnreadCounts(context.Background(), model.Selector{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 2 {
		t.Fatalf("summaries = %#v", summaries)
	}
	byID := map[string]model.UnreadSummary{}
	for _, summary := range summaries {
		byID[summary.ConnectionID] = summary
	}
	if byID["personal"].Error == "" {
		t.Fatalf("personal summary missing resolve error: %#v", byID["personal"])
	}
	// work may fail at IMAP dial in this environment; only require it is present.
	if _, ok := byID["work"]; !ok {
		t.Fatalf("missing work summary: %#v", summaries)
	}
}

func TestTriageMessagesProjectsCompactItems(t *testing.T) {
	application := serviceWithConnections(t, mailConnection("work"))
	application.mailSearchContext = func(context.Context, model.Connection, postmail.SearchOptions) (postmail.SearchResult, error) {
		return postmail.SearchResult{Messages: []model.Message{{
			ConnectionID: "work", Folder: "INBOX", UID: 42,
			From:    []model.Address{{Email: "boss@example.test"}},
			Subject: "Budget", Unread: true, Flagged: true, HasAttachments: true,
			Preview: "Please review",
		}}}, nil
	}
	page, err := application.TriageMessages(context.Background(), model.Selector{}, postmail.SearchOptions{}, 25, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("items = %#v", page.Items)
	}
	item := page.Items[0]
	if item.UID != 42 || item.Subject != "Budget" || !item.Unread || item.Preview != "Please review" {
		t.Fatalf("item = %#v", item)
	}
	encoded, _ := json.Marshal(item)
	if strings.Contains(string(encoded), `"text"`) || strings.Contains(string(encoded), `"html"`) {
		t.Fatalf("triage item leaked body fields: %s", encoded)
	}
}

func TestPrepareForwardVerbatimAttachesRawMIMEWithoutBodyPreview(t *testing.T) {
	t.Setenv("POSTHOUSE_CACHE_KEY", base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	connection := mailConnection("work")
	connection.Identity = model.Identity{Email: "work@example.test"}
	connection.Mail.SMTP = model.SMTPConfig{Address: "localhost:3025", Insecure: true}
	application := serviceWithConnections(t, connection)
	raw := []byte("From: a@example.test\r\nTo: b@example.test\r\nSubject: Original\r\n\r\nsecret original body\r\n")
	application.mailGetMessage = func(context.Context, model.Connection, string, uint32) (postmail.FetchedMessage, error) {
		return postmail.FetchedMessage{
			Detail: model.MessageDetail{Message: model.Message{Subject: "Original"}, Text: "secret original body"},
			Raw:    raw,
		}, nil
	}
	prepared, err := application.PrepareForwardVerbatim(context.Background(), "work", MessageLocator{Folder: "INBOX", UID: 7}, []string{"person@example.test"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Preview["verbatim"] != true {
		t.Fatalf("preview verbatim = %#v", prepared.Preview["verbatim"])
	}
	if prepared.Preview["subject"] != "Fwd: Original" {
		t.Fatalf("subject = %#v", prepared.Preview["subject"])
	}
	if text, _ := prepared.Preview["text"].(string); strings.Contains(text, "secret original body") {
		t.Fatalf("preview leaked original body: %#v", prepared.Preview)
	}
	if _, ok := prepared.Preview["attachments"]; !ok {
		t.Fatalf("preview missing attachments: %#v", prepared.Preview)
	}
	ledger, err := application.ensureState()
	if err != nil {
		t.Fatal(err)
	}
	record, err := ledger.GetOperation(context.Background(), prepared.Token)
	if err != nil {
		t.Fatal(err)
	}
	var payload model.SendMessage
	if err := json.Unmarshal(record.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Attachments) != 1 || payload.Attachments[0].ContentType != "message/rfc822" {
		t.Fatalf("attachments = %#v", payload.Attachments)
	}
	if string(payload.Attachments[0].Data) != string(raw) {
		t.Fatalf("attachment data mismatch")
	}
}

func TestPrepareForwardVerbatimRequiresParts(t *testing.T) {
	t.Setenv("POSTHOUSE_CACHE_KEY", base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	connection := mailConnection("work")
	connection.Identity = model.Identity{Email: "work@example.test"}
	connection.Mail.SMTP = model.SMTPConfig{Address: "localhost:3025", Insecure: true}
	application := serviceWithConnections(t, connection)
	application.mailGetMessage = func(context.Context, model.Connection, string, uint32) (postmail.FetchedMessage, error) {
		return postmail.FetchedMessage{Detail: model.MessageDetail{Message: model.Message{Subject: "Empty"}}}, nil
	}
	if _, err := application.PrepareForwardVerbatim(context.Background(), "work", MessageLocator{Folder: "INBOX", UID: 1}, []string{"person@example.test"}, ""); err == nil || !strings.Contains(err.Error(), "requires original MIME") {
		t.Fatalf("empty verbatim error = %v", err)
	}
}

func TestFinishBatchMailActionPreservesFailures(t *testing.T) {
	result, err := finishBatchMailAction("moved", nil, []map[string]any{{"uid": uint32(1), "error": "boom"}}, map[string]any{"moved": false})
	if err == nil || result["failed"] == nil || result["count"] != 0 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestPolicyDenyBlocksJunkAndCalendarWrite(t *testing.T) {
	t.Setenv("POSTHOUSE_CACHE_KEY", base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	mail := mailConnection("work")
	mail.Mail.Folders.Junk = "Junk"
	cal := model.Connection{ID: "cal", Name: "Cal", Calendar: &model.CalendarConfig{Kind: "caldav", URL: "http://localhost:5232", Username: "work", Secret: model.SecretRef{Env: "CALENDAR_PASSWORD"}, Collections: []model.CalendarCollection{{ID: "team", Path: "/work/team/"}}}}
	application := serviceWithConnections(t, mail, cal)
	if _, err := application.PolicyDeny([]string{"mail.junk", "calendar.write"}); err != nil {
		t.Fatal(err)
	}
	if _, err := application.PrepareMailAction(context.Background(), "work", "mail.junk", MailAction{UID: 1}); err == nil || !strings.Contains(err.Error(), "policy denies mail.junk") {
		t.Fatalf("junk deny error = %v", err)
	}
	if _, err := application.PrepareCalendarWrite(context.Background(), "cal", "calendar.create", model.Event{CollectionID: "team", Title: "Planning", Start: instant(9), End: instant(10)}); err == nil || !strings.Contains(err.Error(), "policy denies calendar.write") {
		t.Fatalf("calendar deny error = %v", err)
	}
}
