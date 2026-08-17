package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/timborovkov/posthouse/internal/config"
	"github.com/timborovkov/posthouse/internal/service"
)

func TestHelpCreditsAuthorAndLicense(t *testing.T) {
	application := testCLI(t, new(bytes.Buffer))
	if err := application.Run(context.Background(), []string{"help"}); err != nil {
		t.Fatalf("help returned error: %v", err)
	}
	output := application.stdout.(*bytes.Buffer).String()
	for _, expected := range []string{"Built by Tim Borovkov", "https://timb.dev", "MIT License"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("help output %q does not contain %q", output, expected)
		}
	}
}

func TestCalendarICSStreamsFileToStdout(t *testing.T) {
	application := testCLI(t, new(bytes.Buffer))
	output := application.stdout.(*bytes.Buffer)
	err := application.Run(context.Background(), []string{
		"calendar", "ics", "--title", "Planning", "--start", "2026-08-17T09:00:00Z", "--end", "2026-08-17T10:00:00Z",
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !strings.HasPrefix(output.String(), "BEGIN:VCALENDAR\r\n") || !strings.Contains(output.String(), "SUMMARY:Planning") {
		t.Fatalf("stdout is not an ICS file: %q", output.String())
	}
}

func TestCalendarICSWritesSecureFileAndRefusesOverwrite(t *testing.T) {
	application := testCLI(t, new(bytes.Buffer))
	path := filepath.Join(t.TempDir(), "planning.ics")
	args := []string{"calendar", "ics", "--title", "Planning", "--start", "2026-08-17T09:00:00Z", "--end", "2026-08-17T10:00:00Z", "--output", path}
	if err := application.Run(context.Background(), args); err != nil {
		t.Fatalf("first Run returned error: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat returned error: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("ICS permissions are %o, want 600", info.Mode().Perm())
	}
	if err := application.Run(context.Background(), args); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second Run returned %v", err)
	}
}

func TestCalendarCancelRequiresStableID(t *testing.T) {
	application := testCLI(t, new(bytes.Buffer))
	err := application.Run(context.Background(), []string{"calendar", "ics", "--method", "cancel", "--title", "Planning", "--start", "2026-08-17T09:00:00Z", "--end", "2026-08-17T10:00:00Z"})
	if err == nil || !strings.Contains(err.Error(), "requires --id") {
		t.Fatalf("cancel without ID returned %v", err)
	}
}

func TestCalendarCancelPreservesCurrentSequence(t *testing.T) {
	application := testCLI(t, new(bytes.Buffer))
	output := application.stdout.(*bytes.Buffer)
	err := application.Run(context.Background(), []string{"calendar", "ics", "--method", "cancel", "--id", "planning", "--sequence", "4", "--title", "Planning", "--start", "2026-08-17T09:00:00Z", "--end", "2026-08-17T10:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "METHOD:CANCEL") || !strings.Contains(output.String(), "SEQUENCE:4") {
		t.Fatalf("cancellation ICS did not preserve sequence:\n%s", output.String())
	}
	for _, invalid := range []string{"-1", "2147483648"} {
		if err := application.Run(context.Background(), []string{"calendar", "ics", "--id", "planning", "--sequence", invalid, "--title", "Planning", "--start", "2026-08-17T09:00:00Z", "--end", "2026-08-17T10:00:00Z"}); err == nil || !strings.Contains(err.Error(), "2147483647") {
			t.Fatalf("invalid sequence %s returned %v", invalid, err)
		}
	}
}

func TestMailMarkRejectsContradictoryFlags(t *testing.T) {
	application := testCLI(t, new(bytes.Buffer))
	for _, flags := range [][]string{{"--read", "--unread"}, {"--flagged", "--unflagged"}} {
		args := append([]string{"mail", "mark", "--connection", "work", "--uid", "1"}, flags...)
		if err := application.Run(context.Background(), args); err == nil || !strings.Contains(err.Error(), "unambiguous") {
			t.Fatalf("mail mark %v returned %v", flags, err)
		}
	}
}

func TestHeadlessCacheRekeyReportsEnvironmentRotation(t *testing.T) {
	t.Setenv("POSTHOUSE_CACHE_KEY", base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("POSTHOUSE_CACHE_KEY_NEW", base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)))
	application := testCLI(t, new(bytes.Buffer))
	if err := application.Run(context.Background(), []string{"cache", "rekey"}); err != nil {
		t.Fatal(err)
	}
	if output := application.stdout.(*bytes.Buffer).String(); !strings.Contains(output, "required_action") || !strings.Contains(output, "POSTHOUSE_CACHE_KEY") {
		t.Fatalf("rekey output = %s", output)
	}
}

func TestCalendarListCursorRecoversDefaultRange(t *testing.T) {
	start, end := "2026-08-15T00:00:00Z", "2026-09-14T00:00:00Z"
	startTime, endTime, err := calendarListRange(start, end, "opaque-cursor", false, false)
	if err != nil || !startTime.IsZero() || !endTime.IsZero() {
		t.Fatalf("recovered defaults start=%v end=%v err=%v", startTime, endTime, err)
	}
	startTime, endTime, err = calendarListRange(start, end, "opaque-cursor", true, true)
	if err != nil || startTime.IsZero() || endTime.IsZero() {
		t.Fatalf("explicit ranges start=%v end=%v err=%v", startTime, endTime, err)
	}
}

func TestCheckedUIDRejectsOverflow(t *testing.T) {
	if _, err := checkedUID(uint64(^uint32(0)) + 1); err == nil || !strings.Contains(err.Error(), "uint32") {
		t.Fatalf("checkedUID overflow error = %v", err)
	}
	if uid, err := checkedUID(uint64(^uint32(0))); err != nil || uid != ^uint32(0) {
		t.Fatalf("checkedUID maximum = %d, %v", uid, err)
	}
}

func testCLI(t *testing.T, stdout *bytes.Buffer) *CLI {
	t.Helper()
	store, err := config.New(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatalf("config.New returned error: %v", err)
	}
	return New(service.New(store), stdout, new(bytes.Buffer))
}
