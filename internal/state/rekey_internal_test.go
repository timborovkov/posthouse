package state

import (
	"context"
	"encoding/base64"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestKeychainRekeyRecoversAfterActivationFailure(t *testing.T) {
	t.Setenv("POSTHOUSE_CACHE_KEY", "")
	oldGet, oldSet, oldLockPath := credentialGet, credentialSet, cacheKeyLockPath
	t.Cleanup(func() { credentialGet, credentialSet, cacheKeyLockPath = oldGet, oldSet, oldLockPath })
	lockPath := filepath.Join(t.TempDir(), "cache-key.lock")
	cacheKeyLockPath = func() (string, error) { return lockPath, nil }
	oldKey, newKey := make([]byte, 32), make([]byte, 32)
	for index := range newKey {
		newKey[index] = 9
	}
	secrets := map[string]string{keyringService + "\x00" + cacheKeyName: base64.RawURLEncoding.EncodeToString(oldKey)}
	failActivation := true
	credentialGet = func(service, name string) (string, error) {
		value, ok := secrets[service+"\x00"+name]
		if !ok {
			return "", keyring.ErrNotFound
		}
		return value, nil
	}
	credentialSet = func(service, name, value string) error {
		if failActivation && service == keyringService && name == cacheKeyName && value == base64.RawURLEncoding.EncodeToString(newKey) {
			return errors.New("simulated keychain interruption")
		}
		secrets[service+"\x00"+name] = value
		return nil
	}

	path := filepath.Join(t.TempDir(), "posthouse.db")
	store, err := OpenWithKey(path, 2<<20, oldKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(context.Background(), CacheEntry{Namespace: "event", Key: "one", Kind: "event", Value: []byte("private")}); err != nil {
		t.Fatal(err)
	}
	if err := store.Rekey(context.Background(), newKey); err == nil || !strings.Contains(err.Error(), "activation failed") {
		t.Fatalf("Rekey returned %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	failActivation = false
	recovered, err := Open(path, 2<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	entry, ok, err := recovered.Get(context.Background(), "event", "one", true)
	if err != nil || !ok || string(entry.Value) != "private" {
		t.Fatalf("recovered cache = %q, %v, %v", entry.Value, ok, err)
	}
	if secrets[keyringService+"\x00"+cacheKeyName] != base64.RawURLEncoding.EncodeToString(newKey) {
		t.Fatal("recovered key was not promoted to the active keychain slot")
	}
	var recoveryRows int
	if err := recovered.db.QueryRow(`SELECT COUNT(*) FROM state_meta WHERE name=?`, rekeyRecoveryName).Scan(&recoveryRows); err != nil || recoveryRows != 0 {
		t.Fatalf("recovery record count=%d err=%v", recoveryRows, err)
	}
}

func TestConcurrentFirstOpenUsesOneKeychainKey(t *testing.T) {
	t.Setenv("POSTHOUSE_CACHE_KEY", "")
	oldGet, oldSet, oldLockPath := credentialGet, credentialSet, cacheKeyLockPath
	t.Cleanup(func() { credentialGet, credentialSet, cacheKeyLockPath = oldGet, oldSet, oldLockPath })
	lockPath := filepath.Join(t.TempDir(), "cache-key.lock")
	cacheKeyLockPath = func() (string, error) { return lockPath, nil }
	var mu sync.Mutex
	secrets := make(map[string]string)
	sets := 0
	credentialGet = func(service, name string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		value, ok := secrets[service+"\x00"+name]
		if !ok {
			return "", keyring.ErrNotFound
		}
		return value, nil
	}
	credentialSet = func(service, name, value string) error {
		mu.Lock()
		defer mu.Unlock()
		sets++
		secrets[service+"\x00"+name] = value
		return nil
	}

	directory := t.TempDir()
	paths := []string{filepath.Join(directory, "one.db"), filepath.Join(directory, "two.db")}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, path := range paths {
		go func(path string) {
			<-start
			store, err := Open(path, 2<<20)
			if err == nil {
				err = store.Close()
			}
			results <- err
		}(path)
	}
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("concurrent Open returned %v", err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if sets != 1 {
		t.Fatalf("cache key stored %d times, want 1", sets)
	}
}
