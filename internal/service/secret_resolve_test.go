package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/timborovkov/posthouse/internal/model"
)

func TestResolveMailConnectionAcceptsCommandSecret(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "secret.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf 'from-command'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	connection := model.Connection{
		ID: "work",
		Mail: &model.MailConfig{
			Username: "me@example.test",
			Secret:   model.SecretRef{Command: []string{script}},
			IMAP:     model.IMAPConfig{Address: "imap.example.test:993", TLS: true},
		},
	}
	resolved, err := resolveMailConnection(connection)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Mail.ResolvedSecret != "from-command" {
		t.Fatalf("resolved = %q", resolved.Mail.ResolvedSecret)
	}
}

func TestMailActionUIDsAndCap(t *testing.T) {
	uids := mailActionUIDs(MailAction{UIDs: []uint32{1, 1, 2, 0}})
	if len(uids) != 2 || uids[0] != 1 || uids[1] != 2 {
		t.Fatalf("uids = %#v", uids)
	}
	single := mailActionUIDs(MailAction{UID: 9})
	if len(single) != 1 || single[0] != 9 {
		t.Fatalf("single uid = %#v", single)
	}
}
