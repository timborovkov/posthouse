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
