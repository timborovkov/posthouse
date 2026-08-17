package mail

import (
	"errors"
	"strings"
	"testing"
)

func TestHTMLToMarkdownBasics(t *testing.T) {
	got := HTMLToMarkdown(`<p>Hello <strong>world</strong></p><ul><li>one</li><li>two</li></ul>`)
	for _, part := range []string{"Hello", "**world**", "- one", "- two"} {
		if !strings.Contains(got, part) {
			t.Fatalf("missing %q in %q", part, got)
		}
	}
}

func TestHTMLToMarkdownLinksAndEntities(t *testing.T) {
	got := HTMLToMarkdown(`<a href="https://example.com/x">the doc</a> &amp; &lt;safe&gt;`)
	if !strings.Contains(got, "[the doc](https://example.com/x)") {
		t.Fatalf("link lost: %q", got)
	}
	if strings.Contains(got, "<safe>") {
		t.Fatalf("entity unescape created raw HTML: %q", got)
	}
	if !strings.Contains(got, "&lt;safe&gt;") {
		t.Fatalf("expected re-escaped angle brackets: %q", got)
	}
}

func TestHTMLToMarkdownDoesNotTreatBlockquoteAsBold(t *testing.T) {
	got := HTMLToMarkdown(`<blockquote>quoted</blockquote>`)
	if strings.Contains(got, "**") {
		t.Fatalf("blockquote became bold: %q", got)
	}
	if !strings.Contains(got, "quoted") {
		t.Fatalf("missing quote text: %q", got)
	}
}

func TestExtractPDFTextRejectsEmpty(t *testing.T) {
	if _, err := ExtractPDFText(nil); err == nil {
		t.Fatal("expected error")
	}
}

func TestExtractPDFTextRecoversMalformed(t *testing.T) {
	if _, err := ExtractPDFText([]byte("%PDF-1.4\n1 0 obj<<>>endobj\ntrailer<<>>\n")); err == nil {
		t.Fatal("malformed PDF should not succeed with empty text")
	}
}

func TestUnreadFromStatus(t *testing.T) {
	count := uint32(4)
	got, err := unreadFromStatus("INBOX", &count, nil)
	if err != nil || got != 4 {
		t.Fatalf("unread = %d, %v", got, err)
	}
	if _, err := unreadFromStatus("INBOX", nil, nil); err == nil || !strings.Contains(err.Error(), "no count") {
		t.Fatalf("nil unseen error = %v", err)
	}
	if _, err := unreadFromStatus("INBOX", &count, errors.New("boom")); err == nil || !strings.Contains(err.Error(), "STATUS UNSEEN") {
		t.Fatalf("status error = %v", err)
	}
}
