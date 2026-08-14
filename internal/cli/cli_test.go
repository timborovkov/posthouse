package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/posthousehq/posthouse/internal/config"
	"github.com/posthousehq/posthouse/internal/service"
)

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

func testCLI(t *testing.T, stdout *bytes.Buffer) *CLI {
	t.Helper()
	store, err := config.New(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatalf("config.New returned error: %v", err)
	}
	return New(service.New(store), stdout, new(bytes.Buffer))
}
