package fsutil

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sub", "test.json")

	data := []byte(`{"hello": "world"}`)
	err := WriteFileAtomic(target, data, 0o644)
	require.NoError(t, err)

	read, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, data, read)
}

func TestWriteFileAtomicOverwritesExisting(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "test.txt")

	err := WriteFileAtomic(target, []byte("initial"), 0o644)
	require.NoError(t, err)

	err = WriteFileAtomic(target, []byte("updated content"), 0o644)
	require.NoError(t, err)

	read, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "updated content", string(read))
}

func TestWriteFileAtomicRapidUpdates(t *testing.T) {
	// Simulate rapid atomic saves as happens during manifest batch commits
	dir := t.TempDir()
	target := filepath.Join(dir, "rapid.json")

	for i := 0; i < 20; i++ {
		content := []byte(string(rune('A' + i)))
		err := WriteFileAtomic(target, content, 0o644)
		require.NoError(t, err)
	}

	read, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, string(rune('A'+19)), string(read))
}

func TestWriteFileAtomicConcurrentWriters(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "concurrent.json")

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(val int) {
			defer wg.Done()
			_ = WriteFileAtomic(target, []byte("val"), 0o644)
		}(i)
	}
	wg.Wait()

	read, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "val", string(read))
}
