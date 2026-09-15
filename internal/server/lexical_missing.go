package server

import (
	"errors"
	"io/fs"
	"os"
)

// lexicalIndexMissing reports whether a corpus holding chunks has no lexical
// index on disk.
//
// lexical.Load reads a missing file as an empty index, so that a corpus built
// before the index existed still serves. The same leniency lets a corpus whose
// build stopped before indexing serve on vector search alone, with nothing at
// startup to say keyword and exact-reference search are off.
func lexicalIndexMissing(path string, vectors int) bool {
	if vectors == 0 {
		return false
	}
	_, err := os.Stat(path)
	return errors.Is(err, fs.ErrNotExist)
}
