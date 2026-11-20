package utils

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nichuanfang/gymdl/config"
)

func resetTestLogger() {
	Sync()
	loggerInstance = nil
	sugaredLoggerInstance = nil
}

func TestInitLoggerWritesFileWithoutConsoleAtInfo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "run.log")
	if err := InitLogger(&config.LogConfig{Mode: 2, Level: 2, File: path, MaxSizeMB: 500}); err != nil {
		t.Fatal(err)
	}
	defer resetTestLogger()
	InfoWithFormat("file logger line %d", 7)
	_ = loggerInstance.Sync()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "file logger line 7") {
		t.Fatalf("file log = %q", data)
	}
}

func TestInitLoggerRotatesAtConfiguredSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.log")
	if err := InitLogger(&config.LogConfig{Mode: 2, Level: 2, File: path, MaxSizeMB: 1}); err != nil {
		t.Fatal(err)
	}
	defer resetTestLogger()

	line := strings.Repeat("x", 128*1024)
	for i := 0; i < 10; i++ {
		InfoWithFormat("%s", line)
	}
	Sync()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "run-") && strings.HasSuffix(entry.Name(), ".gz") {
			return
		}
	}
	t.Fatalf("expected a rotated compressed log in %s, entries = %v", dir, entries)
}
