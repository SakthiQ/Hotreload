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
	// eager starts the rebuild on the first event of a burst instead of
	// waiting out the debounce window first.
	eager bool

	// Trigger carries the time the first event of the burst arrived, so the
	// app can report reload latency as the user experiences it: from the save,
	// not from the end of the debounce window.
	Trigger chan time.Time
}

func New(logger *slog.Logger, root string, debounce time.Duration, excludes, includeExts []string, eager bool) (*Watcher, error) {
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
		eager:       eager,
		Trigger:     make(chan time.Time, 1),
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

	go w.loop(w.watcher.Events, w.watcher.Errors)
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

// loop coalesces filesystem events into rebuild triggers. It takes its
// channels as arguments rather than reading w.watcher directly so that tests
// can drive it without a real fsnotify watcher behind it.
func (w *Watcher) loop(events <-chan fsnotify.Event, errs <-chan error) {
	var (
		timer  *time.Timer
		timerC <-chan time.Time
		// burstStart is when the first event of the current burst arrived.
		// Reload latency is measured from here, so the reported number
		// includes the debounce wait the user actually sat through.
		burstStart time.Time
		// firedLeading records that eager mode already started this burst;
		// pending records that changes have arrived since the last trigger and
		// still need one.
		firedLeading bool
		pending      bool
	)

	stopTimer := func() {
		if timer != nil {
			timer.Stop()
			timer = nil
		}
		timerC = nil
	}

	endBurst := func() {
		burstStart = time.Time{}
		firedLeading = false
		pending = false
	}

	for {
		select {
		case event, ok := <-events:
			if !ok {
				stopTimer()
				return
			}

			// Directory bookkeeping happens for every event, including ones
			// that do not themselves warrant a rebuild.
			w.handleDirEvent(event)

			if !w.wantsRebuild(event) {
				continue
			}

			w.logger.Info("file changed", slog.String("file", event.Name), slog.String("op", event.Op.String()))

			if burstStart.IsZero() {
				burstStart = time.Now()
				firedLeading = false
			}

			if w.eager && !firedLeading {
				// Start immediately; the timer below still runs, so a burst
				// that continues past this point gets a second trigger and the
				// half-written file that the first build may have read is
				// corrected.
				w.fire(burstStart)
				firedLeading = true
				pending = false
			} else {
				pending = true
			}

			stopTimer()
			timer = time.NewTimer(w.debounce)
			timerC = timer.C

		case <-timerC:
			stopTimer()
			if pending {
				w.fire(burstStart)
			}
			endBurst()

		case err, ok := <-errs:
			if !ok {
				stopTimer()
				return
			}
			w.logger.Warn("watcher error", slog.Any("error", err))
		}
	}
}

// fire queues a rebuild. If one is already queued, the changes are simply
// picked up by it.
func (w *Watcher) fire(changedAt time.Time) {
	select {
	case w.Trigger <- changedAt:
		w.logger.Debug("triggering rebuild")
	default:
		w.logger.Debug("rebuild already queued, folding change into it")
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

// wantsRebuild reports whether an event should start a rebuild.
func (w *Watcher) wantsRebuild(event fsnotify.Event) bool {
	// A permission change on its own is noise on some platforms.
	if event.Has(fsnotify.Chmod) &&
		!event.Has(fsnotify.Write) &&
		!event.Has(fsnotify.Create) &&
		!event.Has(fsnotify.Remove) &&
		!event.Has(fsnotify.Rename) {
		return false
	}

	if !w.shouldTrigger(event.Name) {
		w.logger.Debug("ignoring change", slog.String("file", event.Name))
		return false
	}

	return true
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
