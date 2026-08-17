package mail

import "testing"

func TestHTMLToMarkdownBasics(t *testing.T) {
	got := HTMLToMarkdown(`<p>Hello <strong>world</strong></p><ul><li>one</li><li>two</li></ul>`)
	if got == "" || !containsAll(got, "Hello", "**world**", "- one", "- two") {
		t.Fatalf("unexpected markdown: %q", got)
	}
}

func containsAll(value string, parts ...string) bool {
	for _, part := range parts {
		if !containsString(value, part) {
			return false
		}
	}
	return true
}

func containsString(value, part string) bool {
	return len(value) >= len(part) && (value == part || len(part) == 0 || indexOf(value, part) >= 0)
}

func indexOf(value, part string) int {
	for i := 0; i+len(part) <= len(value); i++ {
		if value[i:i+len(part)] == part {
			return i
		}
	}
	return -1
}
