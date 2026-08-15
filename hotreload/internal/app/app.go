package app

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sakthi-narayan/hotreload/internal/builder"
	"github.com/sakthi-narayan/hotreload/internal/config"
	"github.com/sakthi-narayan/hotreload/internal/runner"
	"github.com/sakthi-narayan/hotreload/internal/watcher"
)

const (
	// crashThreshold is how long a process must survive before we consider the
	// start successful. Anything shorter is treated as a crash on startup.
	crashThreshold = 2 * time.Second

	// maxRestarts is how many consecutive crashes we retry before giving up
	// and waiting for the user to change a file.
	maxRestarts = 5

	// baseBackoff is the delay before the first retry; it doubles per crash.
	baseBackoff = time.Second
	maxBackoff  = 30 * time.Second
)

type App struct {
	logger *slog.Logger
	cfg    config.Config

	builder *builder.Builder
	runner  *runner.Runner
	watcher *watcher.Watcher
}

func New(logger *slog.Logger, cfg config.Config) (*App, error) {
	b := builder.New(logger)
	r := runner.New(logger, cfg.KillTimeout, cfg.SettleDelay)
	w, err := watcher.New(logger, cfg.Root, cfg.Debounce, cfg.Exclude, cfg.IncludeExt, cfg.Eager)
	if err != nil {
		return nil, err
	}

	return &App{
		logger:  logger,
		cfg:     cfg,
		builder: b,
		runner:  r,
		watcher: w,
	}, nil
}

func (a *App) Run() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a.triggerRebuild()
	go a.handleSignals(cancel)

	if err := a.watcher.Start(); err != nil {
		a.logger.Error("failed to start watcher", slog.Any("error", err))
		return err
	}
	defer a.watcher.Stop()

	var (
		cancelBuild context.CancelFunc
		retryTimer  *time.Timer
		// A nil channel blocks forever, so retryC is only ever readable while
		// a restart is actually pending.
		retryC  <-chan time.Time
		crashes int
	)

	stopRetry := func() {
		if retryTimer != nil {
			retryTimer.Stop()
			retryTimer = nil
		}
		retryC = nil
	}

	for {
		select {
		case <-ctx.Done():
			if cancelBuild != nil {
				cancelBuild()
			}
			stopRetry()
			if err := a.runner.Stop(); err != nil {
				a.logger.Warn("error stopping process during shutdown", slog.Any("error", err))
			}
			return nil

		case changedAt := <-a.watcher.Trigger:
			a.logger.Info("file change detected, restarting...")
			if cancelBuild != nil {
				cancelBuild()
			}
			// A file changed, so whatever made the process crash may well be
			// fixed: start counting from scratch.
			stopRetry()
			crashes = 0

			if err := a.runner.Stop(); err != nil {
				a.logger.Warn("error stopping process before restart", slog.Any("error", err))
			}
			var buildCtx context.Context
			buildCtx, cancelBuild = context.WithCancel(ctx)
			go a.runBuild(buildCtx, changedAt)

		case ev := <-a.runner.Exited:
			if ev.Uptime >= crashThreshold {
				// It ran long enough to be a real run; the exit is the app's
				// business, not a startup failure.
				crashes = 0
				a.logger.Info("process stopped on its own, waiting for changes",
					slog.Duration("uptime", ev.Uptime))
				continue
			}

			crashes++
			if crashes >= maxRestarts {
				a.logger.Error("process keeps crashing on startup, giving up until the next file change",
					slog.Int("attempts", crashes), slog.Any("error", ev.Err))
				stopRetry()
				continue
			}

			delay := backoff(crashes)
			a.logger.Warn("process crashed on startup, retrying",
				slog.Int("attempt", crashes),
				slog.Duration("uptime", ev.Uptime),
				slog.Duration("retry_in", delay),
				slog.Any("error", ev.Err))
			stopRetry()
			retryTimer = time.NewTimer(delay)
			retryC = retryTimer.C

		case <-retryC:
			retryC = nil
			retryTimer = nil
			// The binary is already built; only the process needs restarting.
			// This is a retry, not a reload, so it is not timed.
			a.startProcess(time.Time{})
		}
	}
}

// backoff returns the delay before retry number n (1-based), doubling each
// time up to maxBackoff.
func backoff(n int) time.Duration {
	if n < 1 {
		n = 1
	}
	delay := baseBackoff
	for i := 1; i < n; i++ {
		delay *= 2
		if delay >= maxBackoff {
			return maxBackoff
		}
	}
	return delay
}

// handleSignals blocks until SIGINT or SIGTERM is received, then cancels the root context.
func (a *App) handleSignals(cancel context.CancelFunc) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	a.logger.Info("shutting down...")
	cancel()
}

// runBuild executes the build command and, on success, starts the child
// process. changedAt is when the triggering file change arrived, and is zero
// for the initial build.
func (a *App) runBuild(ctx context.Context, changedAt time.Time) {
	if err := a.builder.Build(ctx, a.cfg.Root, a.cfg.Build); err != nil {
		if ctx.Err() == nil {
			a.logger.Error("build failed", slog.Any("error", err))
		}
		return
	}
	if ctx.Err() != nil {
		return
	}
	a.startProcess(changedAt)
}

// startProcess launches the built binary. When changedAt is set, the total
// time from the user's save to the process being up is reported: the number
// that actually matters, covering debounce, shutdown, build and startup.
func (a *App) startProcess(changedAt time.Time) {
	if err := a.runner.Start(a.cfg.Root, a.cfg.Exec); err != nil {
		a.logger.Error("failed to start process", slog.Any("error", err))
		return
	}
	if !changedAt.IsZero() {
		a.logger.Info("reload complete", slog.Duration("total", time.Since(changedAt)))
	}
}

func (a *App) triggerRebuild() {
	select {
	case a.watcher.Trigger <- time.Time{}:
	default:
	}
}
