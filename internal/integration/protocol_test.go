//go:build integration

package integration_test

// This suite proves Posthouse behavior against real disposable IMAP, SMTP,
// and CalDAV servers; a mock cannot validate wire semantics or ETag conflicts.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	postcalendar "github.com/timborovkov/posthouse/internal/calendar"
	postmail "github.com/timborovkov/posthouse/internal/mail"
	"github.com/timborovkov/posthouse/internal/model"
)

func TestSMTPToIMAPRoundTripWithAttachment(t *testing.T) {
	t.Setenv("WORK_PASSWORD", requiredEnv(t, "POSTHOUSE_TEST_WORK_PASSWORD"))
	t.Setenv("PERSONAL_PASSWORD", requiredEnv(t, "POSTHOUSE_TEST_PERSONAL_PASSWORD"))
	smtpAddress := requiredEnv(t, "POSTHOUSE_TEST_GREENMAIL_SMTP")
	imapAddress := requiredEnv(t, "POSTHOUSE_TEST_GREENMAIL_IMAP")
	work := mailConnection("work", "work@work.test", "WORK_PASSWORD", smtpAddress, imapAddress)
	personal := mailConnection("personal", "personal@personal.test", "PERSONAL_PASSWORD", smtpAddress, imapAddress)
	subject := fmt.Sprintf("integration-%d", time.Now().UnixNano())
	err := postmail.Send(work, model.SendMessage{
		To: []string{"personal@personal.test"}, Subject: subject, Text: "hello from work",
		Attachments: []model.AttachmentInput{{Name: "note.txt", ContentType: "text/plain", Data: []byte("attachment body")}},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	var found model.Message
	var lastMessages []model.Message
	var lastErr error
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		result, err := postmail.Search(personal, postmail.SearchOptions{Limit: 10})
		lastMessages, lastErr = result.Messages, err
		if err == nil {
			for _, message := range result.Messages {
				if message.Subject == subject {
					found = message
					break
				}
			}
			if found.UID != 0 {
				break
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	if found.UID == 0 {
		t.Fatalf("message %q did not arrive; last messages=%#v error=%v", subject, lastMessages, lastErr)
	}
	fetched, err := postmail.Get(personal, found.Folder, found.UID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if fetched.Detail.Text != "hello from work" || len(fetched.Detail.Attachments) != 1 {
		t.Fatalf("decoded message is %#v", fetched.Detail)
	}
	attachment := fetched.Detail.Attachments[0]
	if string(fetched.Attachments[attachment.ID]) != "attachment body" {
		t.Fatalf("attachment content mismatch")
	}
}

func TestCalDAVDiscoveryCRUDAndETagConflict(t *testing.T) {
	endpoint := requiredEnv(t, "POSTHOUSE_TEST_RADICALE")
	t.Setenv("WORK_CALDAV_PASSWORD", requiredEnv(t, "POSTHOUSE_TEST_WORK_PASSWORD"))
	collectionPath := "/work/work-calendar/"
	ensureCalendar(t, endpoint, collectionPath, "work", requiredEnv(t, "POSTHOUSE_TEST_WORK_PASSWORD"))
	ensureCalendar(t, endpoint, "/work/archive-calendar/", "work", requiredEnv(t, "POSTHOUSE_TEST_WORK_PASSWORD"))
	connection := model.Connection{
		ID: "work", Name: "Work", Identity: model.Identity{Email: "work@work.test"},
		Calendar: &model.CalendarConfig{Kind: "caldav", URL: endpoint, Username: "work", Secret: model.SecretRef{Env: "WORK_CALDAV_PASSWORD"}},
	}
	discovery, err := postcalendar.DiscoverCalDAV(context.Background(), connection)
	if err != nil {
		t.Fatalf("DiscoverCalDAV: %v", err)
	}
	if len(discovery.Calendars) < 2 {
		t.Fatalf("discovered %d calendars; want multiple collections", len(discovery.Calendars))
	}
	var collection, other model.CalendarCollection
	for _, candidate := range discovery.Calendars {
		if candidate.Path == collectionPath {
			collection = candidate
		} else {
			other = candidate
		}
	}
	if collection.ID == "" || other.ID == "" {
		t.Fatalf("unexpected discovered calendars: %#v", discovery.Calendars)
	}
	connection.Calendar.Collections = discovery.Calendars
	event := model.Event{ID: fmt.Sprintf("event-%d", time.Now().UnixNano()), CollectionID: collection.ID, Title: "CalDAV integration", Start: time.Now().UTC().Add(time.Hour).Truncate(time.Second), End: time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second)}
	created, err := postcalendar.PutCalDAVEvent(context.Background(), connection, event, true)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ETag == "" || created.Href == "" {
		t.Fatalf("create returned %#v", created)
	}
	client := postcalendar.NewClient(nil)
	events, err := client.ListCalDAV(context.Background(), connection, []string{collection.ID}, event.Start.Add(-time.Hour), event.End.Add(time.Hour), "integration")
	if err != nil || len(events) != 1 {
		t.Fatalf("ListCalDAV returned %#v, %v", events, err)
	}
	if events, err := client.ListCalDAV(context.Background(), connection, []string{other.ID}, event.Start.Add(-time.Hour), event.End.Add(time.Hour), "integration"); err != nil || len(events) != 0 {
		t.Fatalf("collection-isolated ListCalDAV returned %#v, %v", events, err)
	}
	connection.Calendar.Collections = append(connection.Calendar.Collections, model.CalendarCollection{ID: "missing", Path: "/work/missing-calendar/"})
	partialEvents, partialErr := client.ListCalDAV(context.Background(), connection, nil, event.Start.Add(-time.Hour), event.End.Add(time.Hour), "integration")
	var partial *postcalendar.PartialError
	if !errors.As(partialErr, &partial) || partial.SuccessfulCollections < 2 || len(partial.Errors) != 1 || len(partialEvents) != 1 {
		t.Fatalf("partial CalDAV result events=%#v partial=%#v err=%v", partialEvents, partial, partialErr)
	}
	connection.Calendar.Collections = discovery.Calendars
	conflicting := created
	conflicting.Title = "stale update"
	conflicting.ETag = "definitely-stale"
	if _, err := postcalendar.PutCalDAVEvent(context.Background(), connection, conflicting, false); err == nil {
		t.Fatal("stale ETag update succeeded")
	} else {
		var conflict *postcalendar.ConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("stale update error: %v", err)
		}
	}
	created.Title = "updated"
	updated, err := postcalendar.PutCalDAVEvent(context.Background(), connection, created, false)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := postcalendar.DeleteCalDAVEvent(context.Background(), connection, collection.ID, updated.Href, updated.ETag); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

func mailConnection(id, username, secretEnv, smtpAddress, imapAddress string) model.Connection {
	return model.Connection{ID: id, Name: strings.Title(id), Identity: model.Identity{Email: username}, Mail: &model.MailConfig{Username: username, Secret: model.SecretRef{Env: secretEnv}, IMAP: model.IMAPConfig{Address: imapAddress, Insecure: true}, SMTP: model.SMTPConfig{Address: smtpAddress, Insecure: true}, Folders: model.FolderConfig{Inbox: "INBOX"}, SentCopy: "provider-managed"}}
}

func ensureCalendar(t *testing.T, endpoint, collectionPath, username, password string) {
	t.Helper()
	request, err := http.NewRequest("MKCALENDAR", strings.TrimRight(endpoint, "/")+collectionPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.SetBasicAuth(username, password)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("MKCALENDAR: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusMethodNotAllowed && response.StatusCode != http.StatusConflict {
		t.Fatalf("MKCALENDAR status %s", response.Status)
	}
}

func requiredEnv(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		t.Fatalf("%s is required", name)
	}
	return value
}
