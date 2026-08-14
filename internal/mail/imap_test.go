package mail

import "testing"

func TestPreviewCollapsesWhitespaceAndLimitsLength(t *testing.T) {
	input := []byte("hello\r\n\tworld   " + string(make([]byte, 500)))
	got := preview(input)
	if len([]rune(got)) > 401 {
		t.Fatalf("preview length is %d", len([]rune(got)))
	}
	if got[:11] != "hello world" {
		t.Fatalf("preview returned %q", got)
	}
}
