package watcher

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

// newTestWatcher builds a Watcher without touching the OS watcher, so the
// filtering logic can be tested on its own.
func newTestWatcher(root string, excludes, includeExts []string) *Watcher {
	return &Watcher{
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		root:        root,
		debounce:    time.Millisecond,
		excludes:    excludes,
		includeExts: normalizeExts(includeExts),
		Trigger:     make(chan time.Time, 1),
	}
}

// writeEvent is a plain content change to a watched file.
func writeEvent(path string) fsnotify.Event {
	return fsnotify.Event{Name: filepath.FromSlash(path), Op: fsnotify.Write}
}

func TestIsExcluded(t *testing.T) {
	root := filepath.FromSlash("/project")
	w := newTestWatcher(root, []string{"docs", "internal/generated"}, nil)

	// The root itself is always watched; hidden, build-output and
	// user-excluded directories are not. "documents" is a prefix of the
	// "docs" exclude but a different directory, so it stays watched.
	tests := []struct {
		path string
		want bool
	}{
		{"/project", false},
		{"/project/api", false},
		{"/project/.git", true},
		{"/project/node_modules", true},
		{"/project/tmp", true},
		{"/project/vendor", true},
		{"/project/docs", true},
		{"/project/docs/img", true},
		{"/project/documents", false},
		{"/project/internal", false},
		{"/project/internal/generated", true},
		{"/project/internal/api", false},
	}

	for _, tt := range tests {
		path := filepath.FromSlash(tt.path)
		if got := w.isExcluded(path, filepath.Base(path)); got != tt.want {
			t.Errorf("isExcluded(%s) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

// A project rooted at a directory whose name matches an ignore rule must still
// be watched, otherwise nothing at all is.
func TestIsExcludedRootNamedLikeAnIgnoredDir(t *testing.T) {
	root := filepath.FromSlash("/work/tmp")
	w := newTestWatcher(root, nil, nil)

	if w.isExcluded(root, "tmp") {
		t.Error("isExcluded excluded the root directory itself")
	}
	if !w.isExcluded(filepath.FromSlash("/work/tmp/tmp"), "tmp") {
		t.Error("isExcluded did not exclude a tmp directory below the root")
	}
}

func TestShouldTriggerExtensionFilter(t *testing.T) {
	root := filepath.FromSlash("/project")
	w := newTestWatcher(root, nil, []string{"go", ".mod", "*.html"})

	tests := []struct {
		path string
		want bool
	}{
		{"/project/main.go", true},
		{"/project/go.mod", true},
		{"/project/views/index.html", true},
		// Not in include_ext, build output, hidden file, editor backup.
		{"/project/README.md", false},
		{"/project/server.exe", false},
		{"/project/notes.txt", false},
		{"/project/.env", false},
		{"/project/main.go~", false},
		// Extension matching is case-insensitive, but an ignored directory
		// wins over a matching extension.
		{"/project/MAIN.GO", true},
		{"/project/tmp/main.go", false},
	}

	for _, tt := range tests {
		if got := w.shouldTrigger(filepath.FromSlash(tt.path)); got != tt.want {
			t.Errorf("shouldTrigger(%s) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestShouldTriggerWatchAll(t *testing.T) {
	root := filepath.FromSlash("/project")
	w := newTestWatcher(root, nil, []string{"*"})

	if !w.shouldTrigger(filepath.FromSlash("/project/README.md")) {
		t.Error(`shouldTrigger skipped README.md with include_ext "*"`)
	}
	// Binary build output stays ignored even when watching every file type,
	// or the rebuild it triggers would trigger the next rebuild.
	if w.shouldTrigger(filepath.FromSlash("/project/server.exe")) {
		t.Error(`shouldTrigger accepted server.exe with include_ext "*"`)
	}
}

func TestNormalizeExts(t *testing.T) {
	got := normalizeExts([]string{"go", ".MOD", "*.html", " tmpl "})
	for _, ext := range []string{"go", "mod", "html", "tmpl"} {
		if !got[ext] {
			t.Errorf("normalizeExts did not produce %q (got %v)", ext, got)
		}
	}
	if len(got) != 4 {
		t.Errorf("normalizeExts produced %v, want 4 entries", got)
	}

	if normalizeExts(nil) != nil {
		t.Error("normalizeExts(nil) should disable filtering")
	}
	if normalizeExts([]string{"*"}) != nil {
		t.Error(`normalizeExts(["*"]) should disable filtering`)
	}
}

// startLoop drives the event loop with a channel the test controls, and stops
// it when the test ends.
func startLoop(t *testing.T, w *Watcher) chan fsnotify.Event {
	t.Helper()
	events := make(chan fsnotify.Event, 128)
	errs := make(chan error)
	go w.loop(events, errs)
	t.Cleanup(func() { close(events) })
	return events
}

// Many events arriving in a burst must produce a single rebuild.
func TestDebounceCoalescesEvents(t *testing.T) {
	w := newTestWatcher(filepath.FromSlash("/project"), nil, []string{"go"})
	w.debounce = 60 * time.Millisecond
	events := startLoop(t, w)

	for i := 0; i < 50; i++ {
		events <- writeEvent("/project/main.go")
	}

	select {
	case <-w.Trigger:
		t.Fatal("triggered before the debounce window elapsed")
	case <-time.After(20 * time.Millisecond):
	}

	select {
	case <-w.Trigger:
	case <-time.After(2 * time.Second):
		t.Fatal("no rebuild triggered after the debounce window")
	}

	select {
	case <-w.Trigger:
		t.Fatal("a burst of events produced more than one rebuild")
	case <-time.After(200 * time.Millisecond):
	}
}

// In eager mode the rebuild starts on the first event rather than after the
// debounce window, which is the whole point of the setting.
func TestEagerFiresImmediately(t *testing.T) {
	w := newTestWatcher(filepath.FromSlash("/project"), nil, []string{"go"})
	w.debounce = time.Second
	w.eager = true
	events := startLoop(t, w)

	start := time.Now()
	events <- writeEvent("/project/main.go")

	select {
	case <-w.Trigger:
		if elapsed := time.Since(start); elapsed >= w.debounce {
			t.Errorf("eager mode waited %v, i.e. the full debounce window", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("eager mode did not trigger a rebuild")
	}
}

// A single save in eager mode must not produce a second, wasted rebuild.
func TestEagerSingleEventFiresOnce(t *testing.T) {
	w := newTestWatcher(filepath.FromSlash("/project"), nil, []string{"go"})
	w.debounce = 50 * time.Millisecond
	w.eager = true
	events := startLoop(t, w)

	events <- writeEvent("/project/main.go")

	select {
	case <-w.Trigger:
	case <-time.After(2 * time.Second):
		t.Fatal("eager mode did not trigger a rebuild")
	}

	select {
	case <-w.Trigger:
		t.Error("a single event produced a second rebuild after the window")
	case <-time.After(300 * time.Millisecond):
	}
}

// The trigger carries when the change arrived, so reload latency can be
// measured from the save rather than from the end of the debounce window.
func TestTriggerCarriesChangeTime(t *testing.T) {
	w := newTestWatcher(filepath.FromSlash("/project"), nil, []string{"go"})
	w.debounce = 60 * time.Millisecond
	events := startLoop(t, w)

	before := time.Now()
	events <- writeEvent("/project/main.go")

	select {
	case changedAt := <-w.Trigger:
		if changedAt.Before(before) {
			t.Errorf("changedAt %v predates the event", changedAt)
		}
		// The timestamp must be the arrival of the change, not the moment the
		// debounce window closed.
		if waited := time.Since(changedAt); waited < w.debounce {
			t.Errorf("changedAt looks like the fire time, not the change time (only %v old)", waited)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no rebuild triggered")
	}
}

// Chmod-only events are noise on some platforms and must not rebuild.
func TestIgnoresChmodOnly(t *testing.T) {
	w := newTestWatcher(filepath.FromSlash("/project"), nil, []string{"go"})
	w.debounce = time.Millisecond
	events := startLoop(t, w)

	events <- fsnotify.Event{Name: filepath.FromSlash("/project/main.go"), Op: fsnotify.Chmod}

	select {
	case <-w.Trigger:
		t.Error("a chmod-only event triggered a rebuild")
	case <-time.After(200 * time.Millisecond):
	}
}
