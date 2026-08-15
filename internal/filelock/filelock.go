// Package filelock provides CGo-free cross-process exclusive file locks.
package filelock

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

var pathLocks sync.Map

func Exclusive(path string, action func() error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create lock directory: %w", err)
	}
	value, _ := pathLocks.LoadOrStore(path, &sync.Mutex{})
	mutex := value.(*sync.Mutex)
	mutex.Lock()
	defer mutex.Unlock()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open lock file: %w", err)
	}
	defer file.Close()
	if err := lock(file); err != nil {
		return fmt.Errorf("acquire file lock: %w", err)
	}
	defer unlock(file)
	return action()
}
