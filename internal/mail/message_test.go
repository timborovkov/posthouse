package mail

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/timborovkov/posthouse/internal/model"
)

func TestMessageBodyFetchIsBoundedAtProvider(t *testing.T) {
	section := messageBodySection()
	if !section.Peek || section.Partial != nil {
		t.Fatalf("message body section = %#v", section)
	}
	if _, err := readBoundedLiteral(strings.NewReader("123456"), 5); err == nil {
		t.Fatal("readBoundedLiteral accepted an oversized provider literal")
	}
	data, err := readBoundedLiteral(strings.NewReader("12345"), 5)
	if err != nil || string(data) != "12345" {
		t.Fatalf("readBoundedLiteral = %q, %v", data, err)
	}
}

func TestFlagMutationVerification(t *testing.T) {
	flags := []imap.Flag{imap.FlagSeen}
	if !flagMutationApplied(flags, imap.FlagSeen, true) || !flagMutationApplied(flags, imap.FlagFlagged, false) {
		t.Fatal("flag mutation verifier rejected the requested state")
	}
	if flagMutationApplied(flags, imap.FlagSeen, false) || flagMutationApplied(flags, imap.FlagFlagged, true) {
		t.Fatal("flag mutation verifier accepted an unapplied state")
	}
}

func TestRequestedFlagStateRechecksEveryMutation(t *testing.T) {
	seen, flagged := true, true
	changes := []flagChange{{flag: imap.FlagSeen, value: &seen}, {flag: imap.FlagFlagged, value: &flagged}}
	if requestedFlagStateApplied([]imap.Flag{imap.FlagFlagged}, changes) {
		t.Fatal("final flag verification ignored a reverted earlier mutation")
	}
	if !requestedFlagStateApplied([]imap.Flag{imap.FlagSeen, imap.FlagFlagged}, changes) {
		t.Fatal("final flag verification rejected the complete requested state")
	}
}

func TestSanitizeHTMLUsesParserBasedURLAndAttributeAllowlist(t *testing.T) {
	input := `<p style="background:url(javascript:alert(1))" onclick=alert(1)>Hello</p><a href=javascript:alert(2)>bad</a><a href="jav&#x61;script:alert(3)">encoded</a><a href="https://example.test/path">safe</a><script>alert(4)</script>`
	output := sanitizeHTML(input)
	for _, forbidden := range []string{"javascript", "onclick", "style=", "script", "alert("} {
		if strings.Contains(strings.ToLower(output), forbidden) {
			t.Fatalf("sanitized HTML %q contains %q", output, forbidden)
		}
	}
	if !strings.Contains(output, "https://example.test/path") || !strings.Contains(output, "Hello") {
		t.Fatalf("sanitized HTML removed safe content: %q", output)
	}
}

func TestParseMessagePreservesNonTextInlinePart(t *testing.T) {
	raw := []byte("MIME-Version: 1.0\r\nContent-Type: multipart/related; boundary=part\r\n\r\n--part\r\nContent-Type: text/html\r\n\r\n<img src=\"cid:logo\">\r\n--part\r\nContent-Type: image/png; name=logo.png\r\nContent-Disposition: inline; filename=logo.png\r\nContent-ID: <logo>\r\nContent-Transfer-Encoding: base64\r\n\r\naW1hZ2U=\r\n--part--\r\n")
	parsed, err := parseMessage(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Detail.Attachments) != 1 {
		t.Fatalf("attachments = %#v", parsed.Detail.Attachments)
	}
	attachment := parsed.Detail.Attachments[0]
	if !attachment.Inline || attachment.Name != "logo.png" || attachment.ContentID != "logo" || string(parsed.Attachments[attachment.ID]) != "image" {
		t.Fatalf("inline attachment = %#v bytes=%q", attachment, parsed.Attachments[attachment.ID])
	}
}

func TestParseMessageTreatsNamedInlineTextAsAttachment(t *testing.T) {
	raw := []byte("MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=part\r\n\r\n--part\r\nContent-Type: text/plain\r\n\r\nmessage body\r\n--part\r\nContent-Type: text/plain; name=notes.txt\r\nContent-Disposition: inline; filename=notes.txt\r\n\r\nattached notes\r\n--part--\r\n")
	parsed, err := parseMessage(raw)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Detail.Text != "message body" || len(parsed.Detail.Attachments) != 1 {
		t.Fatalf("parsed message body=%q attachments=%#v", parsed.Detail.Text, parsed.Detail.Attachments)
	}
	attachment := parsed.Detail.Attachments[0]
	if attachment.Name != "notes.txt" || string(parsed.Attachments[attachment.ID]) != "attached notes" {
		t.Fatalf("named text attachment=%#v bytes=%q", attachment, parsed.Attachments[attachment.ID])
	}
}

func TestParseMessageSetsMarkdownFromHTMLAndText(t *testing.T) {
	htmlRaw := []byte("MIME-Version: 1.0\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p>Hello <strong>world</strong></p>\r\n")
	parsed, err := parseMessage(htmlRaw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(parsed.Detail.Markdown, "**world**") {
		t.Fatalf("html markdown = %q", parsed.Detail.Markdown)
	}
	textRaw := []byte("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nplain only\r\n")
	parsed, err = parseMessage(textRaw)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Detail.Markdown != parsed.Detail.Text || !strings.Contains(parsed.Detail.Markdown, "plain only") {
		t.Fatalf("text markdown = %q text = %q", parsed.Detail.Markdown, parsed.Detail.Text)
	}
}

func TestAppendWaitErrorDistinguishesTaggedRejection(t *testing.T) {
	rejected := classifyAppendWaitError(&imap.Error{Type: imap.StatusResponseTypeNo, Text: "over quota"})
	var uncertain *UncertainAppendError
	if errors.As(rejected, &uncertain) {
		t.Fatalf("tagged NO was classified uncertain: %v", rejected)
	}
	ambiguous := classifyAppendWaitError(errors.New("connection closed"))
	if !errors.As(ambiguous, &uncertain) {
		t.Fatalf("transport loss was not classified uncertain: %v", ambiguous)
	}
}

func TestAddressableAppendRequiresUIDPlusAndNonzeroUID(t *testing.T) {
	if supportsAddressableAppend(imap.CapSet{}) {
		t.Fatal("plain IMAP4rev1 unexpectedly supports addressable APPEND")
	}
	if !supportsAddressableAppend(imap.CapSet{imap.CapUIDPlus: {}}) || !supportsAddressableAppend(imap.CapSet{imap.CapIMAP4rev2: {}}) {
		t.Fatal("UIDPLUS or IMAP4rev2 did not enable addressable APPEND")
	}
	for _, result := range []*imap.AppendData{nil, {}} {
		if uid, err := addressableAppendUID(result); uid != 0 || err == nil {
			t.Fatalf("addressableAppendUID(%#v) = %d, %v", result, uid, err)
		} else {
			var uncertain *UncertainAppendError
			if !errors.As(err, &uncertain) {
				t.Fatalf("missing UID error type = %T", err)
			}
		}
	}
	if uid, err := addressableAppendUID(&imap.AppendData{UID: 42}); err != nil || uid != 42 {
		t.Fatalf("addressableAppendUID returned %d, %v", uid, err)
	}
	if uid, err := appendResultUID(nil, false); err != nil || uid != 0 {
		t.Fatalf("non-addressable sent-copy APPEND returned %d, %v", uid, err)
	}
}

func TestDiscoverContextHonorsPreCanceledContext(t *testing.T) {
	t.Setenv("IMAP_PASSWORD", "test-password")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	connection := model.Connection{ID: "work", Mail: &model.MailConfig{Username: "work", Secret: model.SecretRef{Env: "IMAP_PASSWORD"}, IMAP: model.IMAPConfig{Address: "203.0.113.1:993", TLS: true}}}
	started := time.Now()
	_, err := DiscoverContext(ctx, connection)
	if !errors.Is(err, context.Canceled) || time.Since(started) > time.Second {
		t.Fatalf("pre-canceled discovery returned %v after %v", err, time.Since(started))
	}
}

func TestDoctorSMTPHonorsPreCanceledContextDuringDial(t *testing.T) {
	t.Setenv("SMTP_PASSWORD", "test-password")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	connection := model.Connection{ID: "work", Mail: &model.MailConfig{Username: "work", Secret: model.SecretRef{Env: "SMTP_PASSWORD"}, SMTP: model.SMTPConfig{Address: "203.0.113.1:465", TLS: true}}}
	started := time.Now()
	err := DoctorSMTP(ctx, connection)
	if !errors.Is(err, context.Canceled) || time.Since(started) > time.Second {
		t.Fatalf("pre-canceled SMTP doctor returned %v after %v", err, time.Since(started))
	}
}

func TestMutationErrorDistinguishesTaggedRejection(t *testing.T) {
	rejected := classifyMutationError("move", &imap.Error{Type: imap.StatusResponseTypeNo, Text: "rejected"})
	var uncertain *UncertainMutationError
	if errors.As(rejected, &uncertain) {
		t.Fatalf("tagged rejection was uncertain: %v", rejected)
	}
	ambiguous := classifyMutationError("move", errors.New("connection closed"))
	if !errors.As(ambiguous, &uncertain) {
		t.Fatalf("transport loss was not uncertain: %v", ambiguous)
	}
}
