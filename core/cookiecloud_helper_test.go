package core

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteCookieFileReplacesAtomically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cookies.txt")
	if err := writeCookieFile(path, []byte("first")); err != nil {
		t.Fatal(err)
	}
	open, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer open.Close()
	if err = writeCookieFile(path, []byte("second")); err != nil {
		t.Fatal(err)
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// The descriptor opened before the replace still reads the previous inode.
	previous, err := io.ReadAll(open)
	if err != nil {
		t.Fatal(err)
	}
	if string(previous) != "first" || string(current) != "second" {
		t.Fatalf("previous %q current %q", previous, current)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
	leftovers, err := filepath.Glob(filepath.Join(dir, ".cookies.*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) != 0 {
		t.Fatal(leftovers)
	}
}
