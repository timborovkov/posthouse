package safeio

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// WriteFile creates path with mode 0600. It refuses to replace an existing
// file unless force is true.
func WriteFile(path string, data []byte, force bool) (string, error) {
	flags := os.O_WRONLY | os.O_CREATE
	if force {
		flags |= os.O_TRUNC
	} else {
		flags |= os.O_EXCL
	}
	file, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("output file %s already exists; pass --force to replace it", path)
		}
		return "", fmt.Errorf("create output file: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("secure output file: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("write output file: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close output file: %w", err)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return path, nil
	}
	return absolute, nil
}
