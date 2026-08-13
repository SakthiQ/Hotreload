package watcher

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

type Watcher struct {
	logger   *slog.Logger
	root     string
	watcher  *fsnotify.Watcher
	debounce time.Duration
	excludes []string
	// includeExts holds lower-case extensions without the leading dot. When
	// empty, every file that is not explicitly ignored triggers a rebuild.
	includeExts map[string]bool
	Trigger     chan struct{}
}

func New(logger *slog.Logger, root string, debounce time.Duration, excludes, includeExts []string) (*Watcher, error) {
	fsWatcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	return &Watcher{
		logger:      logger,
		root:        root,
		watcher:     fsWatcher,
		debounce:    debounce,
		excludes:    excludes,
		includeExts: normalizeExts(includeExts),
		Trigger:     make(chan struct{}, 1),
	}, nil
}

// normalizeExts accepts extensions written as "go", ".go" or "*.go" and
// returns nil when the caller asked for every file.
func normalizeExts(exts []string) map[string]bool {
	set := make(map[string]bool, len(exts))
	for _, ext := range exts {
		ext = strings.ToLower(strings.TrimSpace(ext))
		ext = strings.TrimPrefix(ext, "*")
		ext = strings.TrimPrefix(ext, ".")
		if ext == "" {
			// A bare "*" means "watch everything"; no filtering at all.
			return nil
		}
		set[ext] = true
	}
	if len(set) == 0 {
		return nil
	}
	return set
}

func (w *Watcher) Start() error {
	w.logger.Info("discovering directories to watch...", slog.String("root", w.root))
	err := w.addRecursive(w.root)
	if err != nil {
		w.watcher.Close()
		return err
	}

	go w.watchLoop()
	return nil
}

func (w *Watcher) Stop() error {
	return w.watcher.Close()
}

func (w *Watcher) isExcluded(path, base string) bool {
	// The root is always watched, whatever it happens to be called. Without
	// this, a project rooted at a hidden or build-output directory name (say
	// ./tmp) would match the ignore rules below and nothing would be watched.
	if filepath.Clean(path) == filepath.Clean(w.root) {
		return false
	}

	if strings.HasPrefix(base, ".") && base != "." && base != ".." {
		return true
	}
	ignoredDirs := map[string]bool{
		"node_modules": true,
		"vendor":       true,
		"bin":          true,
		"build":        true,
		"dist":         true,
		"tmp":          true,
	}
	if ignoredDirs[base] {
		return true
	}

	// Make path relative to root to check user excludes correctly
	relPath, err := filepath.Rel(w.root, path)
	if err != nil {
		relPath = path
	}
	relPath = filepath.ToSlash(relPath)
	for _, excl := range w.excludes {
		exclBase := filepath.ToSlash(excl)
		if relPath == exclBase || strings.HasPrefix(relPath, exclBase+"/") || base == exclBase {
			return true
		}
	}

	return false
}

func (w *Watcher) addRecursive(dir string) error {
	return filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			base := filepath.Base(path)

			if w.isExcluded(path, base) {
				w.logger.Debug("ignoring directory", slog.String("path", path))
				return filepath.SkipDir
			}

			w.logger.Debug("watching directory", slog.String("path", path))
			err = w.watcher.Add(path)
			if err != nil {
				// Check for common OS watcher limits (e.g., too many open files on Linux)
				if strings.Contains(err.Error(), "too many open files") || strings.Contains(err.Error(), "no space left on device") {
					w.logger.Error("OS watcher limit reached!",
						slog.String("path", path),
						slog.Any("error", err),
						slog.String("fix_action", "Use the --exclude flag to ignore large directories or increase your OS inotify/file limits."))
					// We return the error here to halt startup completely, making the issue obvious to the user.
					return err
				}
				w.logger.Warn("failed to watch directory", slog.String("path", path), slog.Any("error", err))
			}
		}
		return nil
	})
}

func (w *Watcher) watchLoop() {
	var timer *time.Timer

	for {
		select {
		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			timer = w.handleEvent(event, timer)

		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			w.logger.Warn("watcher error", slog.Any("error", err))
		}
	}
}

// ignoredExtensions are binary/build-output file types that should never trigger a rebuild.
var ignoredExtensions = map[string]bool{
	".exe":  true,
	".out":  true,
	".bin":  true,
	".o":    true,
	".a":    true,
	".so":   true,
	".test": true,
	".tmp":  true,
	".swp":  true,
}

func (w *Watcher) handleEvent(event fsnotify.Event, timer *time.Timer) *time.Timer {
	if event.Has(fsnotify.Chmod) &&
		!event.Has(fsnotify.Write) &&
		!event.Has(fsnotify.Create) &&
		!event.Has(fsnotify.Remove) &&
		!event.Has(fsnotify.Rename) {
		return timer
	}

	// Directory creation and removal must be tracked even for paths that do
	// not themselves trigger a rebuild, or new packages go unwatched.
	w.handleDirEvent(event)

	if !w.shouldTrigger(event.Name) {
		w.logger.Debug("ignoring change", slog.String("file", event.Name))
		return timer
	}

	w.logger.Info("file changed", slog.String("file", event.Name), slog.String("op", event.Op.String()))

	if timer != nil {
		timer.Stop()
	}
	return time.AfterFunc(w.debounce, func() {
		select {
		case w.Trigger <- struct{}{}:
			w.logger.Debug("triggering rebuild")
		default:
		}
	})
}

// shouldTrigger reports whether a change to path warrants a rebuild.
func (w *Watcher) shouldTrigger(path string) bool {
	base := filepath.Base(path)
	if strings.HasSuffix(base, "~") || strings.HasPrefix(base, ".") {
		return false
	}

	ext := strings.ToLower(filepath.Ext(base))
	if ignoredExtensions[ext] {
		return false
	}

	if w.includeExts != nil && !w.includeExts[strings.TrimPrefix(ext, ".")] {
		return false
	}

	// The change may be inside a directory that is watched only because it was
	// created after startup, or that fsnotify reports on behalf of its parent.
	if dir := filepath.Dir(path); dir != path && w.isExcluded(dir, filepath.Base(dir)) {
		return false
	}

	return true
}

// handleDirEvent watches newly created directories and drops deleted ones.
func (w *Watcher) handleDirEvent(event fsnotify.Event) {
	if event.Has(fsnotify.Create) {
		info, err := os.Stat(event.Name)
		if err == nil && info.IsDir() {
			if w.isExcluded(event.Name, filepath.Base(event.Name)) {
				return
			}
			w.logger.Info("new directory detected, watching", slog.String("path", event.Name))
			if err := w.addRecursive(event.Name); err != nil {
				w.logger.Warn("failed to watch new directory", slog.String("path", event.Name), slog.Any("error", err))
			}
		}
	} else if event.Has(fsnotify.Remove) || event.Has(fsnotify.Rename) {
		_ = w.watcher.Remove(event.Name)
	}
}
