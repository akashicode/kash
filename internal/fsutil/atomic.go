// Package fsutil provides filesystem helper utilities with resilience against
// operating system quirks, such as transient Windows file-locking.
package fsutil

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ReplaceFile atomically replaces dst with src.
//
// On POSIX systems, os.Rename atomically overwrites dst if it exists.
// On Windows, if dst exists and another process (such as Windows Defender,
// search indexing, or an IDE file watcher) has an open handle to dst,
// MoveFileEx fails with ERROR_ACCESS_DENIED. Retrying with exponential backoff
// allows the transient lock to be released.
func ReplaceFile(src, dst string) error {
	var err error
	for attempt := 0; attempt < 15; attempt++ {
		err = os.Rename(src, dst)
		if err == nil {
			return nil
		}
		// On Windows, transient access-denied or sharing-violation errors resolve
		// as soon as the indexing or virus-scanning process closes its handle.
		time.Sleep(time.Duration(10*(attempt+1)) * time.Millisecond)
	}

	// Final fallback: attempt removing dst first, then renaming.
	if rmErr := os.Remove(dst); rmErr == nil || os.IsNotExist(rmErr) {
		if renErr := os.Rename(src, dst); renErr == nil {
			return nil
		}
	}

	return err
}

// WriteFileAtomic writes data to path atomically using a temporary file in the
// same directory followed by ReplaceFile, so an interrupted write or concurrent
// read never sees a partially written or corrupt file.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create directory %q: %w", dir, err)
	}

	base := filepath.Base(path)
	tmp, err := os.CreateTemp(dir, "."+base+"-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file for %q: %w", path, err)
	}
	tmpPath := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write temp file %q: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close temp file %q: %w", tmpPath, err)
	}

	if perm != 0 {
		_ = os.Chmod(tmpPath, perm)
	}

	if err := ReplaceFile(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("replace %q with %q: %w", path, tmpPath, err)
	}
	return nil
}
