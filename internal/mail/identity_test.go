package mail

import (
	"testing"

	"github.com/timborovkov/posthouse/internal/model"
)

func TestIMAPIDRoundTripEncodesFolderUIDAndValidity(t *testing.T) {
	id := EncodeIMAPID("INBOX", 11, 42)
	locator, imap, err := ParseIMAPID(id)
	if err != nil || !imap || locator.Folder != "INBOX" || locator.UIDValidity != 11 || locator.UID != 42 {
		t.Fatalf("ParseIMAPID(%q) = %#v, %v, %v", id, locator, imap, err)
	}
	nested := EncodeIMAPID("Work/Invoices:2026", 7, 9)
	locator, imap, err = ParseIMAPID(nested)
	if err != nil || !imap || locator.Folder != "Work/Invoices:2026" || locator.UID != 9 {
		t.Fatalf("nested folder id = %#v, %v, %v", locator, imap, err)
	}
}

func TestParseIMAPIDRejectsNonIMAPAndBrokenPayloads(t *testing.T) {
	if _, imap, err := ParseIMAPID("18c4d2a0f1"); err != nil || imap {
		t.Fatalf("gmail-shaped id should not parse as IMAP: imap=%v err=%v", imap, err)
	}
	if _, imap, err := ParseIMAPID("imap:not-a-locator"); err == nil || !imap {
		t.Fatalf("broken IMAP id = imap=%v err=%v", imap, err)
	}
	if _, imap, err := ParseIMAPID("imap:1:0:SU5CT1g"); err == nil || !imap {
		t.Fatalf("zero UID IMAP id = imap=%v err=%v", imap, err)
	}
}

func TestStampIMAPMessagesIsIdempotentAndFillsFolder(t *testing.T) {
	messages := []model.Message{{UID: 3}, {ID: "already", Folder: "Sent", UID: 8}}
	StampIMAPMessages(messages, "INBOX", 5)
	if messages[0].ID != EncodeIMAPID("INBOX", 5, 3) || messages[0].Folder != "INBOX" {
		t.Fatalf("stamped first message = %#v", messages[0])
	}
	if messages[1].ID != "already" || messages[1].Folder != "Sent" {
		t.Fatalf("existing id was rewritten: %#v", messages[1])
	}
}
