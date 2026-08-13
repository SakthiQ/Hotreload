package runner

import (
	"io"
	"log/slog"
	"runtime"
	"testing"
	"time"
)

func testRunner() *Runner {
	return New(slog.New(slog.NewTextHandler(io.Discard, nil)), 3*time.Second, 0)
}

// exitImmediately is a command that starts and exits with a failure straight
// away, standing in for a server that crashes on startup.
func exitImmediately() string {
	if runtime.GOOS == "windows" {
		return "cmd /c exit 3"
	}
	return "exit 3"
}

// runForever is a command that keeps running until it is killed.
func runForever() string {
	if runtime.GOOS == "windows" {
		return "cmd /c ping -n 30 127.0.0.1"
	}
	return "sleep 30"
}

func TestStartReportsUnexpectedExit(t *testing.T) {
	r := testRunner()

	if err := r.Start(t.TempDir(), exitImmediately()); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	select {
	case ev := <-r.Exited:
		if ev.Err == nil {
			t.Error("ExitEvent.Err is nil for a process that exited with status 3")
		}
		if ev.Uptime > 5*time.Second {
			t.Errorf("ExitEvent.Uptime = %v, unreasonably long for an immediate exit", ev.Uptime)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no exit event for a process that exited immediately")
	}
}

func TestStopDoesNotReportAnExit(t *testing.T) {
	r := testRunner()

	if err := r.Start(t.TempDir(), runForever()); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	if err := r.Stop(); err != nil {
		t.Fatalf("Stop returned error: %v", err)
	}

	select {
	case ev := <-r.Exited:
		t.Errorf("a deliberate Stop was reported as a crash: %+v", ev)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestStartRejectsASecondProcess(t *testing.T) {
	r := testRunner()

	if err := r.Start(t.TempDir(), runForever()); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	t.Cleanup(func() { _ = r.Stop() })

	if err := r.Start(t.TempDir(), runForever()); err == nil {
		t.Error("Start succeeded while a process was already running")
	}
}

func TestStartRejectsAnEmptyCommand(t *testing.T) {
	r := testRunner()

	if err := r.Start(t.TempDir(), "   "); err == nil {
		t.Error("Start succeeded with an empty exec command")
	}
}

func TestStopWithNoProcessIsANoop(t *testing.T) {
	if err := testRunner().Stop(); err != nil {
		t.Errorf("Stop with no process returned %v", err)
	}
}
