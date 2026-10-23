package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// writeFileAtomic replaces path only after a complete, synced temporary file
// in the same directory has been closed. Failures leave the previous target
// untouched and remove the temporary artifact.
func writeFileAtomic(path string, data []byte, perm os.FileMode) (err error) {
	temporary, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	temporaryName := temporary.Name()
	removeTemporary := true
	defer func() {
		if temporary != nil {
			_ = temporary.Close()
		}
		if removeTemporary {
			_ = os.Remove(temporaryName)
		}
	}()

	if err := temporary.Chmod(perm); err != nil {
		return fmt.Errorf("set temporary file mode: %w", err)
	}
	written, err := temporary.Write(data)
	if err != nil {
		return fmt.Errorf("write temporary file: %w", err)
	}
	if written != len(data) {
		return fmt.Errorf("write temporary file: %w", io.ErrShortWrite)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary file: %w", err)
	}
	temporary = nil

	if err := os.Rename(temporaryName, path); err != nil {
		return fmt.Errorf("replace target: %w", err)
	}
	removeTemporary = false
	return nil
}
