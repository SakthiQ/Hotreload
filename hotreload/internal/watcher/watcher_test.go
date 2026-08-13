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
		Trigger:     make(chan struct{}, 1),
	}
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

// Many events arriving in a burst must produce a single rebuild.
func TestDebounceCoalescesEvents(t *testing.T) {
	w := newTestWatcher(filepath.FromSlash("/project"), nil, []string{"go"})
	w.debounce = 60 * time.Millisecond

	var timer *time.Timer
	for i := 0; i < 50; i++ {
		timer = w.handleEvent(fsnotify.Event{
			Name: filepath.FromSlash("/project/main.go"),
			Op:   fsnotify.Write,
		}, timer)
	}

	select {
	case <-w.Trigger:
		t.Fatal("triggered before the debounce window elapsed")
	case <-time.After(20 * time.Millisecond):
	}

	select {
	case <-w.Trigger:
	case <-time.After(time.Second):
		t.Fatal("no rebuild triggered after the debounce window")
	}

	select {
	case <-w.Trigger:
		t.Fatal("a burst of events produced more than one rebuild")
	case <-time.After(100 * time.Millisecond):
	}
}

// Chmod-only events are noise on some platforms and must not rebuild.
func TestHandleEventIgnoresChmodOnly(t *testing.T) {
	w := newTestWatcher(filepath.FromSlash("/project"), nil, []string{"go"})
	w.debounce = time.Millisecond

	w.handleEvent(fsnotify.Event{
		Name: filepath.FromSlash("/project/main.go"),
		Op:   fsnotify.Chmod,
	}, nil)

	select {
	case <-w.Trigger:
		t.Error("a chmod-only event triggered a rebuild")
	case <-time.After(50 * time.Millisecond):
	}
}
