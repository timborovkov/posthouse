package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/timborovkov/posthouse/internal/model"
)

func TestResolveSecretCommand(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "secret.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf 'command-secret-value\\nmetadata line\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	value, err := ResolveSecret(model.SecretRef{Command: []string{script}})
	if err != nil {
		t.Fatal(err)
	}
	if value != "command-secret-value" {
		t.Fatalf("got %q", value)
	}
}

func TestResolveSecretCommandScrubsPosthouseEnv(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "secret.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nif printenv POSTHOUSE_CACHE_KEY >/dev/null 2>&1; then echo leaked; else echo ok-secret; fi\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("POSTHOUSE_CACHE_KEY", "should-not-leak")
	value, err := ResolveSecret(model.SecretRef{Command: []string{script}})
	if err != nil {
		t.Fatal(err)
	}
	if value != "ok-secret" {
		t.Fatalf("got %q", value)
	}
}

func TestValidSecretRefExactlyOne(t *testing.T) {
	if validSecretRef(model.SecretRef{}) {
		t.Fatal("empty ref should be invalid")
	}
	if !validSecretRef(model.SecretRef{Env: "X"}) {
		t.Fatal("env ref should be valid")
	}
	if validSecretRef(model.SecretRef{Env: "X", Keychain: "y"}) {
		t.Fatal("env+keychain should be invalid")
	}
	if !validSecretRef(model.SecretRef{Command: []string{"pass", "show", "x"}}) {
		t.Fatal("command ref should be valid")
	}
	if validSecretRef(model.SecretRef{Env: "X", Command: []string{"pass"}}) {
		t.Fatal("env+command should be invalid")
	}
}

func TestResolveSecretCommandRejectsEmptyAndControlChars(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.sh")
	if err := os.WriteFile(empty, []byte("#!/bin/sh\nprintf '\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveSecret(model.SecretRef{Command: []string{empty}}); err == nil || !strings.Contains(err.Error(), "empty secret") {
		t.Fatalf("empty secret error = %v", err)
	}
	control := filepath.Join(dir, "control.sh")
	if err := os.WriteFile(control, []byte("#!/bin/sh\nprintf 'a\\000b'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveSecret(model.SecretRef{Command: []string{control}}); err == nil || !strings.Contains(err.Error(), "control characters") {
		t.Fatalf("control secret error = %v", err)
	}
}
