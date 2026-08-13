package builder

import (
	"context"
	"io"
	"log/slog"
	"runtime"
	"strings"
	"testing"
	"time"
)

func testBuilder() *Builder {
	return New(slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func succeedingCmd() string {
	if runtime.GOOS == "windows" {
		return "cmd /c exit 0"
	}
	return "true"
}

func failingCmd() string {
	if runtime.GOOS == "windows" {
		return "cmd /c exit 1"
	}
	return "exit 1"
}

func slowCmd() string {
	if runtime.GOOS == "windows" {
		return "ping -n 30 127.0.0.1"
	}
	return "sleep 30"
}

func TestBuildSuccess(t *testing.T) {
	if err := testBuilder().Build(context.Background(), t.TempDir(), succeedingCmd()); err != nil {
		t.Errorf("Build returned error for a successful command: %v", err)
	}
}

func TestBuildFailure(t *testing.T) {
	err := testBuilder().Build(context.Background(), t.TempDir(), failingCmd())
	if err == nil {
		t.Fatal("Build returned nil for a command that exited non-zero")
	}
	if !strings.Contains(err.Error(), "build failed") {
		t.Errorf("error = %q, want it to mention the build failing", err)
	}
}

func TestBuildEmptyCommand(t *testing.T) {
	if err := testBuilder().Build(context.Background(), t.TempDir(), "  "); err == nil {
		t.Error("Build accepted an empty build command")
	}
}

// A rebuild triggered while an older build is still running cancels it, and
// that cancellation must be distinguishable from a genuine failure.
//
// t.TempDir is load-bearing here: its cleanup fails on Windows if the
// cancelled build left any process alive holding the directory, so this also
// asserts that cancellation takes down the whole process tree.
func TestBuildCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() {
		done <- testBuilder().Build(ctx, t.TempDir(), slowCmd())
	}()

	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Build returned nil for a cancelled build")
		}
		if !strings.Contains(err.Error(), "canceled") {
			t.Errorf("error = %q, want it to report the build as canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Build did not return after its context was cancelled")
	}
}
