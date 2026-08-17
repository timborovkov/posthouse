package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/timborovkov/posthouse/internal/model"
)

func TestResolveSecretCommand(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "secret.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf 'command-secret-value'\n"), 0o700); err != nil {
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
