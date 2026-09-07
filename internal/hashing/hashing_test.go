package hashing_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Kminhas21/concord/internal/hashing"
)

func TestHashFileMatchesKnownSHA256(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "hello.txt")
	if err := os.WriteFile(f, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, exists, err := hashing.HashFile(f)
	if err != nil || !exists {
		t.Fatalf("HashFile: exists=%v err=%v", exists, err)
	}
	// Independent source of truth: the SHA-256 of "hello".
	const want = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if got != want {
		t.Fatalf("HashFile = %q, want %q", got, want)
	}
}

func TestHashFileReportsMissingAsNotExist(t *testing.T) {
	got, exists, err := hashing.HashFile(filepath.Join(t.TempDir(), "nope.txt"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exists || got != "" {
		t.Fatalf("missing file reported exists=%v hash=%q", exists, got)
	}
}
