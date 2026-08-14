package mail

import (
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

func TestFlagPreconditionRefreshRequiresAnotherChange(t *testing.T) {
	value := true
	if hasFlagChange([]flagChange{{flag: imap.FlagFlagged}}) {
		t.Fatal("nil flag change required a MODSEQ refresh")
	}
	if !hasFlagChange([]flagChange{{flag: imap.FlagFlagged, value: &value}}) {
		t.Fatal("non-nil flag change skipped the required MODSEQ refresh")
	}
}
