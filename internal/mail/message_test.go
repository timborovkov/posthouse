package mail

import (
	"errors"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
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
