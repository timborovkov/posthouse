package state_test

// These tests verify encrypted cache round trips and that provider content is
// not left recoverable as plaintext in the SQLite file.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/timborovkov/posthouse/internal/model"
	"github.com/timborovkov/posthouse/internal/state"
)

func TestDefaultPathUsesDocumentedDatabaseName(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	if got, want := state.DefaultPath(configPath, ""), filepath.Join(filepath.Dir(configPath), "posthouse.db"); got != want {
		t.Fatalf("DefaultPath=%q want %q", got, want)
	}
}

func TestEncryptedCacheAndOperationRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := state.OpenWithKey(path, 2<<20, bytesOf(7, 32))
	if err != nil {
		t.Fatal(err)
	}
	secretContent := []byte("Highly confidential subject and body")
	if err := store.Put(context.Background(), state.CacheEntry{Namespace: "message_body", Key: "one", ConnectionID: "work", Kind: "message_body", ExpiresAt: time.Now().Add(time.Hour), Value: secretContent}); err != nil {
		t.Fatal(err)
	}
	entry, ok, err := store.Get(context.Background(), "message_body", "one", false)
	if err != nil || !ok || string(entry.Value) != string(secretContent) {
		t.Fatalf("Get returned %q, %v, %v", entry.Value, ok, err)
	}
	prepared := model.PreparedOperation{Token: "opaque-token", Kind: "mail.send", ConnectionID: "work", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute), Status: "prepared"}
	payload, _ := json.Marshal(map[string]string{"subject": "Secret operation subject"})
	if err := store.PutOperation(context.Background(), state.OperationRecord{Public: prepared, Payload: payload, Digest: "digest"}); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetOperation(context.Background(), prepared.Token)
	if err != nil || got.Public.Kind != "mail.send" {
		t.Fatalf("GetOperation: %#v %v", got, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, plaintext := range []string{string(secretContent), "Secret operation subject", "opaque-token"} {
		if strings.Contains(string(raw), plaintext) {
			t.Fatalf("state database exposed plaintext %q", plaintext)
		}
	}
}

func TestGetPurgesExpiredEntry(t *testing.T) {
	store, err := state.OpenWithKey(filepath.Join(t.TempDir(), "state.db"), 2<<20, bytesOf(4, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.Put(ctx, state.CacheEntry{Namespace: "message_body", Key: "expired", Kind: "message_body", ExpiresAt: time.Now().Add(-time.Minute), Value: []byte("expired secret")}); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(ctx, state.CacheEntry{Namespace: "message_body", Key: "live", Kind: "message_body", ExpiresAt: time.Now().Add(time.Hour), Value: []byte("live")}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.Get(ctx, "message_body", "live", false); err != nil || !ok {
		t.Fatalf("unrelated live Get ok=%v err=%v", ok, err)
	}
	status, err := store.Stats(ctx)
	if err != nil || status.Entries != 1 {
		t.Fatalf("expired entry was not purged: %#v, %v", status, err)
	}
}

func TestClaimOperationRecordsExecutionStart(t *testing.T) {
	store, err := state.OpenWithKey(filepath.Join(t.TempDir(), "state.db"), 2<<20, bytesOf(6, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	prepared := model.PreparedOperation{Token: "claim-token", Kind: "mail.send", ConnectionID: "work", CreatedAt: time.Now().Add(-time.Minute), ExpiresAt: time.Now().Add(time.Minute), Status: "prepared"}
	if err := store.PutOperation(ctx, state.OperationRecord{Public: prepared, Payload: []byte(`{}`), Digest: "digest"}); err != nil {
		t.Fatal(err)
	}
	claimed, won, err := store.ClaimOperation(ctx, prepared.Token)
	if err != nil || !won || claimed.Public.Status != "executing" || claimed.Public.ExecutedAt.IsZero() {
		t.Fatalf("ClaimOperation record=%#v won=%v err=%v", claimed, won, err)
	}
	if !claimed.Public.ExecutedAt.After(prepared.CreatedAt) {
		t.Fatalf("execution start %v was not distinct from preparation", claimed.Public.ExecutedAt)
	}
}

func TestLRUEvictsAttachmentsBeforeBodies(t *testing.T) {
	store, err := state.OpenWithKey(filepath.Join(t.TempDir(), "state.db"), 10, bytesOf(9, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.Put(ctx, state.CacheEntry{Namespace: "message_body", Key: "body", Kind: "message_body", Value: []byte("12345678")}); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(ctx, state.CacheEntry{Namespace: "attachment", Key: "attachment", Kind: "attachment", Value: []byte("abcdefgh")}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.Get(ctx, "attachment", "attachment", true); ok {
		t.Fatal("attachment was not evicted first")
	}
	if _, ok, _ := store.Get(ctx, "message_body", "body", true); !ok {
		t.Fatal("message body was evicted before attachment")
	}
}

func TestUpdatingEntryDoesNotReplaceAnotherEntriesChunks(t *testing.T) {
	store, err := state.OpenWithKey(filepath.Join(t.TempDir(), "state.db"), 2<<20, bytesOf(8, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	for _, entry := range []state.CacheEntry{
		{Namespace: "message_body", Key: "one", Kind: "message_body", Value: []byte("first")},
		{Namespace: "message_body", Key: "two", Kind: "message_body", Value: []byte("second")},
		{Namespace: "message_body", Key: "one", Kind: "message_body", Value: []byte("updated")},
	} {
		if err := store.Put(ctx, entry); err != nil {
			t.Fatal(err)
		}
	}
	for key, want := range map[string]string{"one": "updated", "two": "second"} {
		entry, ok, err := store.Get(ctx, "message_body", key, false)
		if err != nil || !ok || string(entry.Value) != want {
			t.Fatalf("Get(%q) = %q, %v, %v; want %q", key, entry.Value, ok, err, want)
		}
	}
}

func TestRekeyPreservesCacheAndPreparedOperations(t *testing.T) {
	t.Setenv("POSTHOUSE_CACHE_KEY", "managed-by-test")
	path := filepath.Join(t.TempDir(), "state.db")
	oldKey, newKey := bytesOf(3, 32), bytesOf(4, 32)
	store, err := state.OpenWithKey(path, 2<<20, oldKey)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	largeValue := bytesOf(9, (1<<20)+17)
	if err := store.Put(ctx, state.CacheEntry{Namespace: "event", Key: "one", Kind: "event", Value: largeValue}); err != nil {
		t.Fatal(err)
	}
	prepared := model.PreparedOperation{Token: "rekey-token", Kind: "calendar.update", ConnectionID: "work", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute), Status: "prepared"}
	if err := store.PutOperation(ctx, state.OperationRecord{Public: prepared, Payload: []byte(`{"title":"private"}`), Digest: "digest"}); err != nil {
		t.Fatal(err)
	}
	second := prepared
	second.Token = "rekey-token-two"
	if err := store.PutOperation(ctx, state.OperationRecord{Public: second, Payload: []byte(`{"title":"second"}`), Digest: "digest-two"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Rekey(ctx, newKey); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := state.OpenWithKey(path, 2<<20, oldKey); err == nil || !strings.Contains(err.Error(), "cache key does not match") {
		t.Fatalf("opening rekeyed state with old key returned %v", err)
	}
	reopened, err := state.OpenWithKey(path, 2<<20, newKey)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	entry, ok, err := reopened.Get(ctx, "event", "one", false)
	if err != nil || !ok || !bytes.Equal(entry.Value, largeValue) {
		t.Fatalf("multi-chunk cache after rekey = %d bytes, %v, %v", len(entry.Value), ok, err)
	}
	operation, err := reopened.GetOperation(ctx, prepared.Token)
	if err != nil || string(operation.Payload) != `{"title":"private"}` {
		t.Fatalf("operation after rekey = %#v, %v", operation, err)
	}
	operation, err = reopened.GetOperation(ctx, second.Token)
	if err != nil || string(operation.Payload) != `{"title":"second"}` {
		t.Fatalf("second operation after rekey = %#v, %v", operation, err)
	}
}

func TestRekeyRejectsWritesFromStoreWithStaleKey(t *testing.T) {
	t.Setenv("POSTHOUSE_CACHE_KEY", "managed-by-test")
	path := filepath.Join(t.TempDir(), "state.db")
	oldKey, newKey := bytesOf(7, 32), bytesOf(8, 32)
	current, err := state.OpenWithKey(path, 2<<20, oldKey)
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	stale, err := state.OpenWithKey(path, 2<<20, oldKey)
	if err != nil {
		t.Fatal(err)
	}
	defer stale.Close()

	if err := current.Rekey(context.Background(), newKey); err != nil {
		t.Fatal(err)
	}
	err = stale.Put(context.Background(), state.CacheEntry{Namespace: "event", Key: "stale", Kind: "event", Value: []byte("old-key ciphertext")})
	if err == nil || !strings.Contains(err.Error(), "cache key changed") {
		t.Fatalf("stale Put returned %v", err)
	}

	reopened, err := state.OpenWithKey(path, 2<<20, newKey)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, found, err := reopened.Get(context.Background(), "event", "stale", true); err != nil || found {
		t.Fatalf("stale cache entry found=%v, err=%v", found, err)
	}
}

func TestPreparedOperationCanOnlyBeClaimedOnceAcrossStores(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	key := bytesOf(6, 32)
	first, err := state.OpenWithKey(path, 2<<20, key)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := state.OpenWithKey(path, 2<<20, key)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	prepared := model.PreparedOperation{Token: "claim-once", Kind: "mail.send", ConnectionID: "work", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute), Status: "prepared"}
	if err := first.PutOperation(context.Background(), state.OperationRecord{Public: prepared, Payload: []byte(`{}`), Digest: "digest"}); err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	results := make(chan bool, 2)
	for _, store := range []*state.Store{first, second} {
		wait.Add(1)
		go func(store *state.Store) {
			defer wait.Done()
			_, claimed, err := store.ClaimOperation(context.Background(), prepared.Token)
			if err != nil {
				t.Errorf("ClaimOperation: %v", err)
			}
			results <- claimed
		}(store)
	}
	wait.Wait()
	close(results)
	claimed := 0
	for result := range results {
		if result {
			claimed++
		}
	}
	if claimed != 1 {
		t.Fatalf("claimed %d times; want exactly once", claimed)
	}
}

func TestConcurrentStoreOpenWaitsForMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	key := bytesOf(4, 32)
	start := make(chan struct{})
	results := make(chan error, 16)
	var wait sync.WaitGroup
	for range 16 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			store, err := state.OpenWithKey(path, 2<<20, key)
			if err == nil {
				err = store.Close()
			}
			results <- err
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("concurrent OpenWithKey: %v", err)
		}
	}
}

func TestPreparedOperationsRespectStateLimit(t *testing.T) {
	store, err := state.OpenWithKey(filepath.Join(t.TempDir(), "state.db"), 128, bytesOf(5, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	prepared := model.PreparedOperation{Token: "too-large", Kind: "mail.send", ConnectionID: "work", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute), Status: "prepared"}
	err = store.PutOperation(context.Background(), state.OperationRecord{Public: prepared, Payload: []byte(`"` + strings.Repeat("private payload", 100) + `"`), Digest: "digest"})
	if err == nil || !strings.Contains(err.Error(), "state exceeds") {
		t.Fatalf("PutOperation returned %v", err)
	}
	stats, err := store.Stats(context.Background())
	if err != nil || stats.Operations != 0 {
		t.Fatalf("Stats after rejected operation = %#v, %v", stats, err)
	}
}

func TestWrongKeyFailsWhenOpeningState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := state.OpenWithKey(path, 2<<20, bytesOf(1, 32))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := state.OpenWithKey(path, 2<<20, bytesOf(2, 32)); err == nil || !strings.Contains(err.Error(), "cache key does not match") {
		t.Fatalf("OpenWithKey returned %v", err)
	}
}

func bytesOf(value byte, count int) []byte {
	result := make([]byte, count)
	for i := range result {
		result[i] = value
	}
	return result
}
