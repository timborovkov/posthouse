package safeio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteFileCreatesSecureFileAndRefusesOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "note.txt")
	got, err := WriteFile(path, []byte("hello"), false)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(got)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("permissions are %o, want 600", info.Mode().Perm())
	}
	if _, err := WriteFile(path, []byte("other"), false); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("overwrite without force returned %v", err)
	}
	if _, err := WriteFile(path, []byte("other"), true); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "other" {
		t.Fatalf("forced write = %q, %v", data, err)
	}
}
