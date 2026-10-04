package utils

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestCommandContextStopsChildProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script cancellation test is POSIX-specific")
	}
	script := filepath.Join(t.TempDir(), "long-running-command")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec sleep 10\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := CommandContext(ctx, script).Run()
	if err == nil || ctx.Err() == nil {
		t.Fatalf("expected the child process to be terminated by context, err=%v context=%v", err, ctx.Err())
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("child process did not stop promptly after context cancellation: %s", elapsed)
	}
}
