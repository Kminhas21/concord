// Package hashing computes the content hashes the version check compares. The
// hook client hashes files here; the daemon never reads files, so both sides of
// a comparison share exactly one hashing function.
package hashing

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
)

// HashFile returns the hex-encoded SHA-256 of the file at path. exists is false
// (and hash empty) when the file does not exist — the sentinel CheckEdit reads
// as "new file". Any other I/O error is returned.
func HashFile(path string) (hash string, exists bool, err error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), true, nil
}
