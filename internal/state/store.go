// Package state owns Posthouse's encrypted local cache and prepared-operation ledger.
package state

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/zalando/go-keyring"
	"golang.org/x/crypto/chacha20poly1305"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/timborovkov/posthouse/internal/filelock"
	"github.com/timborovkov/posthouse/internal/model"
)

const (
	keyringService              = "posthouse.cache"
	legacyKeyringService        = "posthouse"
	cacheKeyName                = "cache-master-key"
	rekeyRecoveryName           = "rekey-recovery"
	chunkSize                   = 1 << 20
	operationCompletionOverhead = 4 << 10
)

var (
	credentialGet    = keyring.Get
	credentialSet    = keyring.Set
	cacheKeyLockPath = func() (string, error) {
		cacheRoot, err := os.UserCacheDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(cacheRoot, "posthouse", "cache-master-key.lock"), nil
	}
)

type Store struct {
	db       *sql.DB
	keyMu    sync.RWMutex
	key      []byte
	maxBytes int64
	path     string
}

type CacheEntry struct {
	Namespace    string
	Key          string
	ConnectionID string
	Kind         string
	ProviderID   string
	CachedAt     time.Time
	ExpiresAt    time.Time
	Value        []byte
}

type Stats struct {
	Path        string    `json:"path"`
	Entries     int64     `json:"entries"`
	Operations  int64     `json:"operations"`
	Bytes       int64     `json:"bytes"`
	MaxBytes    int64     `json:"max_bytes"`
	OldestEntry time.Time `json:"oldest_entry,omitempty"`
	NewestEntry time.Time `json:"newest_entry,omitempty"`
}

type OperationRecord struct {
	Public       model.PreparedOperation `json:"public"`
	Payload      json.RawMessage         `json:"payload"`
	Digest       string                  `json:"digest"`
	Precondition string                  `json:"precondition,omitempty"`
}

func DefaultPath(configPath, configured string) string {
	if configured != "" {
		return configured
	}
	return filepath.Join(filepath.Dir(configPath), "posthouse.db")
}

func Open(path string, maxBytes int64) (*Store, error) {
	key, err := serializedMasterKey(path)
	if err != nil {
		return nil, err
	}
	store, openErr := OpenWithKey(path, maxBytes, key)
	if openErr == nil {
		_, _ = store.db.Exec(`DELETE FROM state_meta WHERE name=?`, rekeyRecoveryName)
		return store, nil
	}
	if os.Getenv("POSTHOUSE_CACHE_KEY") != "" {
		return nil, openErr
	}
	recoveredKey, recoveryErr := recoverRekeyKey(path, key)
	if recoveryErr != nil {
		return nil, openErr
	}
	store, err = OpenWithKey(path, maxBytes, recoveredKey)
	if err != nil {
		return nil, openErr
	}
	if err := credentialSet(keyringService, cacheKeyCredentialName(path), base64.RawURLEncoding.EncodeToString(recoveredKey)); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("recover rekeyed cache master key in OS keychain: %w", err)
	}
	_, _ = store.db.Exec(`DELETE FROM state_meta WHERE name=?`, rekeyRecoveryName)
	return store, nil
}

func serializedMasterKey(path string) ([]byte, error) {
	if os.Getenv("POSTHOUSE_CACHE_KEY") != "" {
		return masterKey(path)
	}
	lockPath, err := cacheKeyLockPath()
	if err != nil {
		return nil, fmt.Errorf("find cache directory for master-key lock: %w", err)
	}
	var key []byte
	err = filelock.Exclusive(lockPath, func() error {
		var keyErr error
		key, keyErr = masterKey(path)
		return keyErr
	})
	return key, err
}

func recoverRekeyKey(path string, oldKey []byte) ([]byte, error) {
	db, err := openDatabase(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var ciphertext []byte
	if err := db.QueryRow(`SELECT ciphertext FROM state_meta WHERE name=?`, rekeyRecoveryName).Scan(&ciphertext); err != nil {
		return nil, err
	}
	key, err := open(oldKey, ciphertext, []byte("posthouse-state-rekey-recovery"))
	if err != nil || len(key) != chacha20poly1305.KeySize {
		return nil, fmt.Errorf("cache rekey recovery record is invalid")
	}
	return key, nil
}

func OpenWithKey(path string, maxBytes int64, key []byte) (*Store, error) {
	if len(key) != chacha20poly1305.KeySize {
		return nil, fmt.Errorf("cache key must be %d bytes", chacha20poly1305.KeySize)
	}
	if maxBytes <= 0 {
		maxBytes = 2 << 30
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}
	db, err := openDatabase(path)
	if err != nil {
		return nil, fmt.Errorf("open state database: %w", err)
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db, key: append([]byte(nil), key...), maxBytes: maxBytes, path: path}
	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.verifyKey(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.purgeExpired(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("secure state database: %w", err)
	}
	return store, nil
}

func openDatabase(path string) (*sql.DB, error) {
	dsn := url.URL{Scheme: "file", Path: path}
	query := dsn.Query()
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "foreign_keys(1)")
	dsn.RawQuery = query.Encode()
	return sql.Open("sqlite", dsn.String())
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	deadline := time.Now().Add(5 * time.Second)
	statements := []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA busy_timeout=5000`,
		`PRAGMA foreign_keys=ON`,
		`CREATE TABLE IF NOT EXISTS cache_entries (
			id INTEGER PRIMARY KEY, namespace TEXT NOT NULL, key_hash BLOB NOT NULL,
			connection_id TEXT NOT NULL DEFAULT '', kind TEXT NOT NULL DEFAULT '', provider_id TEXT NOT NULL DEFAULT '',
			cached_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, accessed_at INTEGER NOT NULL,
			size_bytes INTEGER NOT NULL, ciphertext BLOB,
			UNIQUE(namespace, key_hash)
		)`,
		`CREATE TABLE IF NOT EXISTS cache_chunks (
			entry_id INTEGER NOT NULL REFERENCES cache_entries(id) ON DELETE CASCADE,
			sequence INTEGER NOT NULL, size_bytes INTEGER NOT NULL, ciphertext BLOB NOT NULL,
			PRIMARY KEY(entry_id, sequence)
		)`,
		`CREATE TABLE IF NOT EXISTS operations (
			token_hash BLOB PRIMARY KEY, kind TEXT NOT NULL, connection_id TEXT NOT NULL,
			created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, executed_at INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL, ciphertext BLOB NOT NULL, reserved_bytes INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS state_meta (
			name TEXT PRIMARY KEY, ciphertext BLOB NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS cache_lru ON cache_entries(kind, accessed_at)`,
		`CREATE INDEX IF NOT EXISTS operations_expiry ON operations(expires_at)`,
	}
	for _, statement := range statements {
		for {
			if _, err := s.db.ExecContext(ctx, statement); err == nil {
				break
			} else if !sqliteBusy(err) || !time.Now().Before(deadline) {
				return fmt.Errorf("migrate state database: %w", err)
			}
			timer := time.NewTimer(25 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return fmt.Errorf("migrate state database: %w", ctx.Err())
			case <-timer.C:
			}
		}
	}
	if err := s.ensureOperationReservationColumn(ctx); err != nil {
		return err
	}
	return nil
}

func (s *Store) ensureOperationReservationColumn(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(operations)`)
	if err != nil {
		return fmt.Errorf("inspect operation schema: %w", err)
	}
	found := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, dataType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return fmt.Errorf("inspect operation column: %w", err)
		}
		found = found || name == "reserved_bytes"
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("inspect operation schema: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("inspect operation schema: %w", err)
	}
	if !found {
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE operations ADD COLUMN reserved_bytes INTEGER NOT NULL DEFAULT 0`); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
			return fmt.Errorf("add operation reservation column: %w", err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE operations SET reserved_bytes=CASE WHEN status IN ('prepared','executing') THEN 2*length(ciphertext)+? ELSE length(ciphertext) END WHERE reserved_bytes=0`, operationCompletionOverhead); err != nil {
		return fmt.Errorf("initialize operation reservations: %w", err)
	}
	return nil
}

func sqliteBusy(err error) bool {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	code := sqliteErr.Code() & 0xff
	return code == sqlite3.SQLITE_BUSY || code == sqlite3.SQLITE_LOCKED
}

func (s *Store) Put(ctx context.Context, entry CacheEntry) error {
	s.keyMu.RLock()
	defer s.keyMu.RUnlock()
	if entry.Namespace == "" || entry.Key == "" {
		return fmt.Errorf("cache namespace and key are required")
	}
	if err := s.purgeExpired(ctx); err != nil {
		return err
	}
	if entry.CachedAt.IsZero() {
		entry.CachedAt = time.Now().UTC()
	}
	if entry.ExpiresAt.IsZero() {
		entry.ExpiresAt = entry.CachedAt.Add(30 * 24 * time.Hour)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin cache write: %w", err)
	}
	defer tx.Rollback()
	if err := s.lockAndVerifyKey(ctx, tx); err != nil {
		return err
	}
	if err := s.ensureCacheEntryFitsTx(ctx, tx, len(entry.Value)); err != nil {
		return err
	}
	if err := s.putEntryTx(ctx, tx, entry); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit cache write: %w", err)
	}
	return s.evict(ctx)
}

// Mutate atomically reads, transforms, and replaces one encrypted cache entry.
func (s *Store) Mutate(ctx context.Context, entry CacheEntry, transform func(current []byte, found bool) ([]byte, error)) error {
	s.keyMu.RLock()
	defer s.keyMu.RUnlock()
	if entry.Namespace == "" || entry.Key == "" {
		return fmt.Errorf("cache namespace and key are required")
	}
	if entry.CachedAt.IsZero() {
		entry.CachedAt = time.Now().UTC()
	}
	if entry.ExpiresAt.IsZero() {
		entry.ExpiresAt = entry.CachedAt.Add(30 * 24 * time.Hour)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin cache mutation: %w", err)
	}
	defer tx.Rollback()
	if err := s.lockAndVerifyKey(ctx, tx); err != nil {
		return err
	}
	current, found, err := s.readEntryValueTx(ctx, tx, entry.Namespace, entry.Key)
	if err != nil {
		return err
	}
	entry.Value, err = transform(current, found)
	if err != nil {
		return err
	}
	if err := s.ensureCacheEntryFitsTx(ctx, tx, len(entry.Value)); err != nil {
		return err
	}
	if err := s.putEntryTx(ctx, tx, entry); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit cache mutation: %w", err)
	}
	return s.evict(ctx)
}

func (s *Store) ensureCacheEntryFitsTx(ctx context.Context, tx *sql.Tx, valueBytes int) error {
	var operationBytes int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(reserved_bytes),0) FROM operations`).Scan(&operationBytes); err != nil {
		return fmt.Errorf("read operation reservations: %w", err)
	}
	if operationBytes+int64(valueBytes) > s.maxBytes {
		return fmt.Errorf("cache entry cannot fit within configured %d-byte encrypted state limit", s.maxBytes)
	}
	return nil
}

func (s *Store) readEntryValueTx(ctx context.Context, tx *sql.Tx, namespace, key string) ([]byte, bool, error) {
	keyHash := cacheKeyHash(namespace, key)
	var id, expiresAt int64
	err := tx.QueryRowContext(ctx, `SELECT id,expires_at FROM cache_entries WHERE namespace=? AND key_hash=?`, namespace, keyHash).Scan(&id, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && time.Now().Unix() > expiresAt) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read cache entry for mutation: %w", err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT sequence,ciphertext FROM cache_chunks WHERE entry_id=? ORDER BY sequence`, id)
	if err != nil {
		return nil, false, fmt.Errorf("read cache chunks for mutation: %w", err)
	}
	defer rows.Close()
	var value []byte
	for rows.Next() {
		var sequence int
		var ciphertext []byte
		if err := rows.Scan(&sequence, &ciphertext); err != nil {
			return nil, false, fmt.Errorf("scan cache chunk for mutation: %w", err)
		}
		plaintext, err := open(s.key, ciphertext, chunkAAD(namespace, keyHash, sequence))
		if err != nil {
			return nil, false, fmt.Errorf("decrypt cache chunk for mutation: %w", err)
		}
		value = append(value, plaintext...)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("iterate cache chunks for mutation: %w", err)
	}
	return value, true, nil
}

func (s *Store) putEntryTx(ctx context.Context, tx *sql.Tx, entry CacheEntry) error {
	keyHash := cacheKeyHash(entry.Namespace, entry.Key)
	var id int64
	err := tx.QueryRowContext(ctx, `INSERT INTO cache_entries
		(namespace,key_hash,connection_id,kind,provider_id,cached_at,expires_at,accessed_at,size_bytes,ciphertext)
		VALUES(?,?,?,?,?,?,?,?,?,NULL)
		ON CONFLICT(namespace,key_hash) DO UPDATE SET connection_id=excluded.connection_id,kind=excluded.kind,
		provider_id=excluded.provider_id,cached_at=excluded.cached_at,expires_at=excluded.expires_at,
		accessed_at=excluded.accessed_at,size_bytes=excluded.size_bytes,ciphertext=NULL
		RETURNING id`,
		entry.Namespace, keyHash, entry.ConnectionID, entry.Kind, entry.ProviderID,
		entry.CachedAt.Unix(), entry.ExpiresAt.Unix(), entry.CachedAt.Unix(), len(entry.Value)).Scan(&id)
	if err != nil {
		return fmt.Errorf("write cache entry: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM cache_chunks WHERE entry_id=?`, id); err != nil {
		return fmt.Errorf("replace cache chunks: %w", err)
	}
	for sequence, offset := 0, 0; offset < len(entry.Value) || (len(entry.Value) == 0 && sequence == 0); sequence++ {
		end := min(offset+chunkSize, len(entry.Value))
		ciphertext, err := seal(s.key, entry.Value[offset:end], chunkAAD(entry.Namespace, keyHash, sequence))
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO cache_chunks(entry_id,sequence,size_bytes,ciphertext) VALUES(?,?,?,?)`, id, sequence, end-offset, ciphertext); err != nil {
			return fmt.Errorf("write cache chunk: %w", err)
		}
		offset = end
		if len(entry.Value) == 0 {
			break
		}
	}
	return nil
}

func (s *Store) Get(ctx context.Context, namespace, key string, allowExpired bool) (CacheEntry, bool, error) {
	s.keyMu.RLock()
	defer s.keyMu.RUnlock()
	if err := s.purgeExpired(ctx); err != nil {
		return CacheEntry{}, false, err
	}
	keyHash := cacheKeyHash(namespace, key)
	var entry CacheEntry
	var id, cachedAt, expiresAt int64
	err := s.db.QueryRowContext(ctx, `SELECT id,connection_id,kind,provider_id,cached_at,expires_at FROM cache_entries WHERE namespace=? AND key_hash=?`, namespace, keyHash).
		Scan(&id, &entry.ConnectionID, &entry.Kind, &entry.ProviderID, &cachedAt, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return CacheEntry{}, false, nil
	}
	if err != nil {
		return CacheEntry{}, false, fmt.Errorf("read cache entry: %w", err)
	}
	entry.Namespace, entry.Key = namespace, key
	entry.CachedAt, entry.ExpiresAt = time.Unix(cachedAt, 0).UTC(), time.Unix(expiresAt, 0).UTC()
	if !allowExpired && time.Now().After(entry.ExpiresAt) {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM cache_entries WHERE id=?`, id); err != nil {
			return CacheEntry{}, false, fmt.Errorf("purge expired cache entry: %w", err)
		}
		return CacheEntry{}, false, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT sequence,ciphertext FROM cache_chunks WHERE entry_id=? ORDER BY sequence`, id)
	if err != nil {
		return CacheEntry{}, false, fmt.Errorf("read cache chunks: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var sequence int
		var ciphertext []byte
		if err := rows.Scan(&sequence, &ciphertext); err != nil {
			return CacheEntry{}, false, fmt.Errorf("scan cache chunk: %w", err)
		}
		plaintext, err := open(s.key, ciphertext, chunkAAD(namespace, keyHash, sequence))
		if err != nil {
			return CacheEntry{}, false, fmt.Errorf("decrypt cache chunk: %w", err)
		}
		entry.Value = append(entry.Value, plaintext...)
	}
	if err := rows.Err(); err != nil {
		return CacheEntry{}, false, fmt.Errorf("iterate cache chunks: %w", err)
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE cache_entries SET accessed_at=? WHERE id=?`, time.Now().Unix(), id)
	return entry, true, nil
}

func (s *Store) Stats(ctx context.Context) (Stats, error) {
	if err := s.purgeExpired(ctx); err != nil {
		return Stats{}, err
	}
	stats := Stats{Path: s.path, MaxBytes: s.maxBytes}
	var oldest, newest sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(size_bytes),0),MIN(cached_at),MAX(cached_at) FROM cache_entries`).Scan(&stats.Entries, &stats.Bytes, &oldest, &newest); err != nil {
		return Stats{}, fmt.Errorf("read cache status: %w", err)
	}
	var operationBytes int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(reserved_bytes),0) FROM operations`).Scan(&stats.Operations, &operationBytes); err != nil {
		return Stats{}, fmt.Errorf("read operation status: %w", err)
	}
	stats.Bytes += operationBytes
	if oldest.Valid {
		stats.OldestEntry = time.Unix(oldest.Int64, 0).UTC()
	}
	if newest.Valid {
		stats.NewestEntry = time.Unix(newest.Int64, 0).UTC()
	}
	return stats, nil
}

func (s *Store) purgeExpired(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM cache_entries WHERE expires_at < ?`, time.Now().Unix()); err != nil {
		return fmt.Errorf("purge expired cache entries: %w", err)
	}
	return nil
}

func (s *Store) Clear(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM cache_entries`); err != nil {
		return fmt.Errorf("clear cache: %w", err)
	}
	return nil
}

func (s *Store) ClearConnection(ctx context.Context, connectionID string) error {
	if connectionID == "" {
		return fmt.Errorf("connection ID is required")
	}
	s.keyMu.RLock()
	defer s.keyMu.RUnlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin connection cache clear: %w", err)
	}
	defer tx.Rollback()
	if err := s.lockAndVerifyKey(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM cache_entries WHERE connection_id=?`, connectionID); err != nil {
		return fmt.Errorf("clear connection cache: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit connection cache clear: %w", err)
	}
	return nil
}

func (s *Store) Delete(ctx context.Context, namespace, key string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM cache_entries WHERE namespace=? AND key_hash=?`, namespace, cacheKeyHash(namespace, key)); err != nil {
		return fmt.Errorf("delete cache entry: %w", err)
	}
	return nil
}

func (s *Store) PutOperation(ctx context.Context, record OperationRecord) error {
	s.keyMu.RLock()
	defer s.keyMu.RUnlock()
	if record.Public.Token == "" {
		return fmt.Errorf("operation token is required")
	}
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode operation: %w", err)
	}
	tokenHash := sha256.Sum256([]byte(record.Public.Token))
	ciphertext, err := seal(s.key, data, tokenHash[:])
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin prepared operation write: %w", err)
	}
	defer tx.Rollback()
	if err := s.lockAndVerifyKey(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM operations WHERE expires_at < ?`, time.Now().Add(-24*time.Hour).Unix()); err != nil {
		return fmt.Errorf("purge expired prepared operations: %w", err)
	}
	var operationBytes int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(reserved_bytes),0) FROM operations`).Scan(&operationBytes); err != nil {
		return fmt.Errorf("measure prepared operations: %w", err)
	}
	reservation := int64(2*len(ciphertext) + operationCompletionOverhead)
	if operationBytes+reservation > s.maxBytes {
		return fmt.Errorf("encrypted state exceeds configured %d-byte limit", s.maxBytes)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO operations(token_hash,kind,connection_id,created_at,expires_at,executed_at,status,ciphertext,reserved_bytes) VALUES(?,?,?,?,?,?,?,?,?)`,
		tokenHash[:], record.Public.Kind, record.Public.ConnectionID, record.Public.CreatedAt.Unix(), record.Public.ExpiresAt.Unix(), 0, record.Public.Status, ciphertext, reservation)
	if err != nil {
		return fmt.Errorf("store prepared operation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit prepared operation: %w", err)
	}
	if err := s.evict(ctx); err != nil {
		tokenHash := sha256.Sum256([]byte(record.Public.Token))
		_, _ = s.db.ExecContext(ctx, `DELETE FROM operations WHERE token_hash=?`, tokenHash[:])
		return err
	}
	return nil
}

func (s *Store) GetOperation(ctx context.Context, token string) (OperationRecord, error) {
	s.keyMu.RLock()
	defer s.keyMu.RUnlock()
	return s.getOperation(ctx, token)
}

func (s *Store) getOperation(ctx context.Context, token string) (OperationRecord, error) {
	tokenHash := sha256.Sum256([]byte(token))
	var ciphertext []byte
	if err := s.db.QueryRowContext(ctx, `SELECT ciphertext FROM operations WHERE token_hash=?`, tokenHash[:]).Scan(&ciphertext); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return OperationRecord{}, fmt.Errorf("prepared operation does not exist")
		}
		return OperationRecord{}, fmt.Errorf("read prepared operation: %w", err)
	}
	data, err := open(s.key, ciphertext, tokenHash[:])
	if err != nil {
		return OperationRecord{}, fmt.Errorf("decrypt prepared operation: %w", err)
	}
	var record OperationRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return OperationRecord{}, fmt.Errorf("decode prepared operation: %w", err)
	}
	return record, nil
}

func (s *Store) UpdateOperation(ctx context.Context, record OperationRecord) error {
	s.keyMu.RLock()
	defer s.keyMu.RUnlock()
	return s.updateOperation(ctx, record, "")
}

func (s *Store) CompleteOperation(ctx context.Context, record OperationRecord) error {
	s.keyMu.RLock()
	defer s.keyMu.RUnlock()
	return s.updateOperation(ctx, record, "executing")
}

func (s *Store) updateOperation(ctx context.Context, record OperationRecord, expectedStatus string) error {
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode operation result: %w", err)
	}
	tokenHash := sha256.Sum256([]byte(record.Public.Token))
	ciphertext, err := seal(s.key, data, tokenHash[:])
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin prepared operation update: %w", err)
	}
	defer tx.Rollback()
	if err := s.lockAndVerifyKey(ctx, tx); err != nil {
		return err
	}
	var existingReservation int64
	if err := tx.QueryRowContext(ctx, `SELECT reserved_bytes FROM operations WHERE token_hash=?`, tokenHash[:]).Scan(&existingReservation); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("prepared operation does not exist")
		}
		return fmt.Errorf("read prepared operation reservation: %w", err)
	}
	if int64(len(ciphertext)) > existingReservation {
		return fmt.Errorf("operation result exceeds its reserved encrypted state capacity")
	}
	reservation := existingReservation
	if record.Public.Status != "prepared" && record.Public.Status != "executing" {
		reservation = int64(len(ciphertext))
	}
	var otherReservations int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(reserved_bytes),0) FROM operations WHERE token_hash<>?`, tokenHash[:]).Scan(&otherReservations); err != nil {
		return fmt.Errorf("measure operation reservations: %w", err)
	}
	if otherReservations+reservation > s.maxBytes {
		return fmt.Errorf("encrypted state exceeds configured %d-byte limit", s.maxBytes)
	}
	query := `UPDATE operations SET status=?,executed_at=?,ciphertext=?,reserved_bytes=? WHERE token_hash=?`
	arguments := []any{record.Public.Status, record.Public.ExecutedAt.Unix(), ciphertext, reservation, tokenHash[:]}
	if expectedStatus != "" {
		query += ` AND status=?`
		arguments = append(arguments, expectedStatus)
	}
	result, err := tx.ExecContext(ctx, query, arguments...)
	if err != nil {
		return fmt.Errorf("update prepared operation: %w", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		if expectedStatus != "" {
			return errOperationNotClaimed
		}
		return fmt.Errorf("prepared operation does not exist")
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit prepared operation update: %w", err)
	}
	return nil
}

var errOperationNotClaimed = errors.New("prepared operation was claimed by another executor")

// ClaimOperation atomically transitions a prepared operation to executing.
// The returned record is authoritative when claimed is false.
func (s *Store) ClaimOperation(ctx context.Context, token string) (record OperationRecord, claimed bool, err error) {
	s.keyMu.RLock()
	defer s.keyMu.RUnlock()
	record, err = s.getOperation(ctx, token)
	if err != nil || record.Public.Status != "prepared" {
		return record, false, err
	}
	record.Public.Status = "executing"
	record.Public.ExecutedAt = time.Now().UTC()
	if err := s.updateOperation(ctx, record, "prepared"); err != nil {
		if !errors.Is(err, errOperationNotClaimed) {
			return OperationRecord{}, false, err
		}
		record, err = s.getOperation(ctx, token)
		return record, false, err
	}
	return record, true, nil
}

func (s *Store) Rekey(ctx context.Context, newKey []byte) error {
	s.keyMu.Lock()
	defer s.keyMu.Unlock()
	if len(newKey) != chacha20poly1305.KeySize {
		return fmt.Errorf("new cache key must be %d bytes", chacha20poly1305.KeySize)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin cache rekey: %w", err)
	}
	defer tx.Rollback()
	if err := s.lockAndVerifyKey(ctx, tx); err != nil {
		return err
	}
	useKeychain := os.Getenv("POSTHOUSE_CACHE_KEY") == ""
	if useKeychain {
		recovery, err := seal(s.key, newKey, []byte("posthouse-state-rekey-recovery"))
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO state_meta(name,ciphertext) VALUES(?,?) ON CONFLICT(name) DO UPDATE SET ciphertext=excluded.ciphertext`, rekeyRecoveryName, recovery); err != nil {
			return fmt.Errorf("store cache rekey recovery record: %w", err)
		}
	}
	type encryptedChunk struct {
		entryID          int64
		sequence         int
		namespace        string
		hash, ciphertext []byte
	}
	lastEntryID, lastSequence := int64(0), -1
	for {
		rows, err := tx.QueryContext(ctx, `SELECT c.entry_id,c.sequence,e.namespace,e.key_hash,c.ciphertext FROM cache_chunks c JOIN cache_entries e ON e.id=c.entry_id WHERE c.entry_id>? OR (c.entry_id=? AND c.sequence>?) ORDER BY c.entry_id,c.sequence LIMIT 1`, lastEntryID, lastEntryID, lastSequence)
		if err != nil {
			return fmt.Errorf("list cache entries for rekey: %w", err)
		}
		var chunk encryptedChunk
		found := rows.Next()
		if found {
			err = rows.Scan(&chunk.entryID, &chunk.sequence, &chunk.namespace, &chunk.hash, &chunk.ciphertext)
		}
		rowErr := rows.Err()
		closeErr := rows.Close()
		if err != nil {
			return err
		}
		if rowErr != nil {
			return rowErr
		}
		if closeErr != nil {
			return closeErr
		}
		if !found {
			break
		}
		aad := chunkAAD(chunk.namespace, chunk.hash, chunk.sequence)
		plaintext, err := open(s.key, chunk.ciphertext, aad)
		if err != nil {
			return fmt.Errorf("decrypt cache during rekey: %w", err)
		}
		ciphertext, err := seal(newKey, plaintext, aad)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE cache_chunks SET ciphertext=? WHERE entry_id=? AND sequence=?`, ciphertext, chunk.entryID, chunk.sequence); err != nil {
			return err
		}
		lastEntryID, lastSequence = chunk.entryID, chunk.sequence
	}
	type encryptedOperation struct{ hash, ciphertext []byte }
	var lastOperationHash []byte
	for {
		query := `SELECT token_hash,ciphertext FROM operations ORDER BY token_hash LIMIT 1`
		var args []any
		if lastOperationHash != nil {
			query = `SELECT token_hash,ciphertext FROM operations WHERE token_hash>? ORDER BY token_hash LIMIT 1`
			args = append(args, lastOperationHash)
		}
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("list operations for rekey: %w", err)
		}
		var value encryptedOperation
		found := rows.Next()
		if found {
			err = rows.Scan(&value.hash, &value.ciphertext)
		}
		rowErr := rows.Err()
		closeErr := rows.Close()
		if err != nil {
			return err
		}
		if rowErr != nil {
			return rowErr
		}
		if closeErr != nil {
			return closeErr
		}
		if !found {
			break
		}
		plaintext, err := open(s.key, value.ciphertext, value.hash)
		if err != nil {
			return err
		}
		ciphertext, err := seal(newKey, plaintext, value.hash)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE operations SET ciphertext=? WHERE token_hash=?`, ciphertext, value.hash); err != nil {
			return err
		}
		lastOperationHash = append(lastOperationHash[:0], value.hash...)
	}
	var marker []byte
	if err := tx.QueryRowContext(ctx, `SELECT ciphertext FROM state_meta WHERE name='key-check'`).Scan(&marker); err != nil {
		return fmt.Errorf("read cache key marker during rekey: %w", err)
	}
	markerPlaintext, err := open(s.key, marker, []byte("posthouse-state-key-check"))
	if err != nil {
		return fmt.Errorf("decrypt cache key marker during rekey: %w", err)
	}
	marker, err = seal(newKey, markerPlaintext, []byte("posthouse-state-key-check"))
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE state_meta SET ciphertext=? WHERE name='key-check'`, marker); err != nil {
		return fmt.Errorf("update cache key marker during rekey: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.key = append(s.key[:0], newKey...)
	if useKeychain {
		if err := credentialSet(keyringService, cacheKeyCredentialName(s.path), base64.RawURLEncoding.EncodeToString(newKey)); err != nil {
			return fmt.Errorf("cache was rekeyed but OS keychain activation failed; restart Posthouse to recover the committed key: %w", err)
		}
		_, _ = s.db.ExecContext(ctx, `DELETE FROM state_meta WHERE name=?`, rekeyRecoveryName)
	}
	return nil
}

type keyVerifier interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// lockAndVerifyKey serializes encrypted writers with rekey and rejects stale
// Store instances before they can persist ciphertext under an obsolete key.
func (s *Store) lockAndVerifyKey(ctx context.Context, executor keyVerifier) error {
	if _, err := executor.ExecContext(ctx, `UPDATE state_meta SET ciphertext=ciphertext WHERE name='key-check'`); err != nil {
		return fmt.Errorf("lock encrypted state: %w", err)
	}
	var marker []byte
	if err := executor.QueryRowContext(ctx, `SELECT ciphertext FROM state_meta WHERE name='key-check'`).Scan(&marker); err != nil {
		return fmt.Errorf("read cache key marker: %w", err)
	}
	plaintext, err := open(s.key, marker, []byte("posthouse-state-key-check"))
	if err != nil || string(plaintext) != "posthouse-state-key-ok" {
		return fmt.Errorf("cache key changed; restart Posthouse with the current cache key")
	}
	return nil
}

func (s *Store) verifyKey(ctx context.Context) error {
	const markerText = "posthouse-state-key-ok"
	aad := []byte("posthouse-state-key-check")
	var marker []byte
	err := s.db.QueryRowContext(ctx, `SELECT ciphertext FROM state_meta WHERE name='key-check'`).Scan(&marker)
	if errors.Is(err, sql.ErrNoRows) {
		if err := s.verifyExistingCiphertext(ctx); err != nil {
			return err
		}
		marker, err = seal(s.key, []byte(markerText), aad)
		if err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO state_meta(name,ciphertext) VALUES('key-check',?)`, marker); err != nil {
			return fmt.Errorf("initialize cache key marker: %w", err)
		}
		if err := s.db.QueryRowContext(ctx, `SELECT ciphertext FROM state_meta WHERE name='key-check'`).Scan(&marker); err != nil {
			return fmt.Errorf("read cache key marker: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("read cache key marker: %w", err)
	}
	plaintext, err := open(s.key, marker, aad)
	if err != nil || string(plaintext) != markerText {
		return fmt.Errorf("cache key does not match the encrypted state database")
	}
	return nil
}

func (s *Store) verifyExistingCiphertext(ctx context.Context) error {
	var namespace string
	var keyHash, ciphertext []byte
	var sequence int
	err := s.db.QueryRowContext(ctx, `SELECT e.namespace,e.key_hash,c.sequence,c.ciphertext FROM cache_chunks c JOIN cache_entries e ON e.id=c.entry_id LIMIT 1`).
		Scan(&namespace, &keyHash, &sequence, &ciphertext)
	if err == nil {
		if _, err := open(s.key, ciphertext, chunkAAD(namespace, keyHash, sequence)); err != nil {
			return fmt.Errorf("cache key does not match the encrypted state database")
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("verify existing cache encryption: %w", err)
	}
	var tokenHash []byte
	err = s.db.QueryRowContext(ctx, `SELECT token_hash,ciphertext FROM operations LIMIT 1`).Scan(&tokenHash, &ciphertext)
	if err == nil {
		if _, err := open(s.key, ciphertext, tokenHash); err != nil {
			return fmt.Errorf("cache key does not match the encrypted state database")
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("verify existing operation encryption: %w", err)
	}
	return nil
}

func (s *Store) evict(ctx context.Context) error {
	stats, err := s.Stats(ctx)
	if err != nil || stats.Bytes <= s.maxBytes {
		return err
	}
	for _, kind := range []string{"attachment", "message_body", "event", "message_metadata"} {
		for stats.Bytes > s.maxBytes {
			result, err := s.db.ExecContext(ctx, `DELETE FROM cache_entries WHERE id=(SELECT id FROM cache_entries WHERE kind=? ORDER BY accessed_at LIMIT 1)`, kind)
			if err != nil {
				return fmt.Errorf("evict cache: %w", err)
			}
			count, _ := result.RowsAffected()
			if count == 0 {
				break
			}
			stats, err = s.Stats(ctx)
			if err != nil {
				return err
			}
		}
	}
	for stats.Bytes > s.maxBytes {
		result, err := s.db.ExecContext(ctx, `DELETE FROM cache_entries WHERE id=(SELECT id FROM cache_entries ORDER BY accessed_at LIMIT 1)`)
		if err != nil {
			return fmt.Errorf("evict cache state: %w", err)
		}
		count, _ := result.RowsAffected()
		if count == 0 {
			break
		}
		stats, err = s.Stats(ctx)
		if err != nil {
			return err
		}
	}
	if stats.Bytes > s.maxBytes {
		return fmt.Errorf("encrypted state exceeds configured %d-byte limit", s.maxBytes)
	}
	return nil
}

func cacheKeyCredentialName(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		absolute = filepath.Clean(path)
	}
	digest := sha256.Sum256([]byte(filepath.Clean(absolute)))
	return cacheKeyName + "-" + hex.EncodeToString(digest[:])
}

func masterKey(path string) ([]byte, error) {
	if encoded := os.Getenv("POSTHOUSE_CACHE_KEY"); encoded != "" {
		return decodeKey(encoded)
	}
	credentialName := cacheKeyCredentialName(path)
	encoded, err := credentialGet(keyringService, credentialName)
	if err == nil {
		return decodeKey(encoded)
	}
	if !errors.Is(err, keyring.ErrNotFound) {
		return nil, fmt.Errorf("resolve cache key from OS keychain (or set POSTHOUSE_CACHE_KEY for headless use): %w", err)
	}
	encoded, err = credentialGet(keyringService, cacheKeyName)
	if err == nil {
		key, decodeErr := decodeKey(encoded)
		if decodeErr != nil {
			return nil, fmt.Errorf("migrate shared cache key: %w", decodeErr)
		}
		if err := credentialSet(keyringService, credentialName, encoded); err != nil {
			return nil, fmt.Errorf("pin shared cache key to state path: %w", err)
		}
		return key, nil
	}
	if !errors.Is(err, keyring.ErrNotFound) {
		return nil, fmt.Errorf("resolve shared cache key: %w", err)
	}
	encoded, err = credentialGet(legacyKeyringService, cacheKeyName)
	if err == nil {
		key, decodeErr := decodeKey(encoded)
		if decodeErr != nil {
			return nil, fmt.Errorf("migrate legacy cache key: %w", decodeErr)
		}
		if err := credentialSet(keyringService, credentialName, encoded); err != nil {
			return nil, fmt.Errorf("migrate cache key to isolated credential namespace: %w", err)
		}
		return key, nil
	}
	if !errors.Is(err, keyring.ErrNotFound) {
		return nil, fmt.Errorf("resolve legacy cache key: %w", err)
	}
	key := make([]byte, chacha20poly1305.KeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate cache key: %w", err)
	}
	if err := credentialSet(keyringService, credentialName, base64.RawURLEncoding.EncodeToString(key)); err != nil {
		return nil, fmt.Errorf("store cache key in OS keychain (or set POSTHOUSE_CACHE_KEY for headless use): %w", err)
	}
	return key, nil
}

func decodeKey(encoded string) ([]byte, error) {
	encoded = strings.TrimSpace(encoded)
	for _, encoding := range []*base64.Encoding{base64.RawURLEncoding, base64.StdEncoding, base64.RawStdEncoding} {
		if key, err := encoding.DecodeString(encoded); err == nil && len(key) == chacha20poly1305.KeySize {
			return key, nil
		}
	}
	if key, err := hex.DecodeString(encoded); err == nil && len(key) == chacha20poly1305.KeySize {
		return key, nil
	}
	return nil, fmt.Errorf("POSTHOUSE_CACHE_KEY must be a base64 or hex encoded 32-byte key")
}

func seal(key, plaintext, aad []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("initialize cache encryption: %w", err)
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate encryption nonce: %w", err)
	}
	return append(nonce, aead.Seal(nil, nonce, plaintext, aad)...), nil
}

func open(key, ciphertext, aad []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < aead.NonceSize() {
		return nil, fmt.Errorf("encrypted value is truncated")
	}
	nonce, data := ciphertext[:aead.NonceSize()], ciphertext[aead.NonceSize():]
	return aead.Open(nil, nonce, data, aad)
}

func cacheKeyHash(namespace, key string) []byte {
	digest := sha256.Sum256([]byte(namespace + "\x00" + key))
	return digest[:]
}

func chunkAAD(namespace string, keyHash []byte, sequence int) []byte {
	return fmt.Appendf(nil, "%s:%x:%d", namespace, keyHash, sequence)
}
