package mail

import (
	"errors"
	"net/textproto"
	"reflect"
	"strings"
	"testing"

	"github.com/timborovkov/posthouse/internal/model"
)

func TestDataCloseErrorDistinguishesSMTPRejection(t *testing.T) {
	rejected := classifyDataCloseError(&textproto.Error{Code: 550, Msg: "message rejected"})
	var uncertain *UncertainError
	if errors.As(rejected, &uncertain) {
		t.Fatalf("definitive SMTP rejection was uncertain: %v", rejected)
	}
	ambiguous := classifyDataCloseError(errors.New("connection closed"))
	if !errors.As(ambiguous, &uncertain) {
		t.Fatalf("transport loss after DATA was not uncertain: %v", ambiguous)
	}
}

func TestBuildMessagePropagatesValidationErrors(t *testing.T) {
	for _, message := range []model.SendMessage{
		{To: []string{"not an address"}, Subject: "invalid"},
		{To: []string{"person@example.test"}, Subject: "invalid attachment", Attachments: []model.AttachmentInput{{Data: []byte("body")}}},
	} {
		if _, err := buildMessage(model.Identity{Email: "sender@example.test"}, "sender@example.test", message); err == nil {
			t.Fatalf("buildMessage accepted %#v", message)
		}
	}
}

func TestSendRejectsInvalidMIMEBeforeConnecting(t *testing.T) {
	connection := model.Connection{ID: "work", Identity: model.Identity{Email: "sender@example.test"}, Mail: &model.MailConfig{Username: "sender@example.test", SMTP: model.SMTPConfig{Address: "unreachable.invalid:25", Insecure: true}}}
	err := Send(connection, model.SendMessage{To: []string{"person@example.test"}, Attachments: []model.AttachmentInput{{Data: []byte("body")}}})
	if err == nil || !strings.Contains(err.Error(), "attachment name is required") {
		t.Fatalf("Send returned %v", err)
	}
}

func TestBuildMessageDoesNotExposeBCCAndNormalizesHeaders(t *testing.T) {
	encoded, err := buildMessage(model.Identity{Name: "Tim", Email: "tim@example.com"}, "tim@example.com", model.SendMessage{
		To: []string{"one@example.com"}, BCC: []string{"hidden@example.com"}, Subject: "Hello\r\nBcc: attacker@example.com", Text: "line one\nline two",
	})
	if err != nil {
		t.Fatal(err)
	}
	data := string(encoded)
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

func TestBuildDraftMessagePreservesBCC(t *testing.T) {
	connection := model.Connection{Identity: model.Identity{Email: "sender@example.com"}}
	message := model.SendMessage{To: []string{"one@example.com"}, BCC: []string{"hidden@example.com"}, Subject: "Draft", Text: "body"}
	draft, err := BuildDraftMessage(connection, message)
	if err != nil {
		t.Fatal(err)
	}
	sent, err := BuildMessage(connection, message)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(draft), "Bcc: <hidden@example.com>") {
		t.Fatalf("draft omitted Bcc header: %q", draft)
	}
	if strings.Contains(string(sent), "Bcc:") {
		t.Fatalf("SMTP message exposed Bcc header: %q", sent)
	}
}

func TestBuildSentCopyPreservesBCCWithoutChangingSerializedMessage(t *testing.T) {
	connection := model.Connection{Identity: model.Identity{Email: "sender@example.com"}}
	message := model.SendMessage{To: []string{"one@example.com"}, BCC: []string{"Hidden Person <hidden@example.com>"}, Subject: "Sent", Text: "body"}
	smtpData, err := BuildMessage(connection, message)
	if err != nil {
		t.Fatal(err)
	}
	copyData, err := BuildSentCopy(smtpData, message.BCC)
	if err != nil {
		t.Fatal(err)
	}
	bccHeader := "\r\nBcc: \"Hidden Person\" <hidden@example.com>"
	if !strings.Contains(string(copyData), bccHeader) {
		t.Fatalf("sent copy omitted Bcc header: %q", copyData)
	}
	withoutBCC := strings.Replace(string(copyData), bccHeader, "", 1)
	if withoutBCC != string(smtpData) {
		t.Fatal("adding Bcc regenerated or changed other serialized message bytes")
	}
}

func TestSMTPHost(t *testing.T) {
	got, err := smtpHost("smtp.example.com:465")
	if err != nil || got != "smtp.example.com" {
		t.Fatalf("smtpHost returned %q, %v", got, err)
	}
}

func TestEnvelopeRecipientsDeduplicateParsedAddresses(t *testing.T) {
	got, err := envelopeRecipients([]string{"Alice <alice@example.test>", "alice@example.test", "ALICE@EXAMPLE.TEST", "bob@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"alice@example.test", "bob@example.test"}) {
		t.Fatalf("envelope recipients = %#v", got)
	}
}
