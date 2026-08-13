package runner

import (
	"fmt"
	"log/slog"
	"os/exec"
	"sync"
	"time"
)

// ExitEvent reports a child process that exited on its own, i.e. one that was
// not stopped by us. Uptime lets the caller tell a crash-on-startup from a
// server that ran fine for a while.
type ExitEvent struct {
	Err    error
	Uptime time.Duration
}

type Runner struct {
	logger      *slog.Logger
	cmd         *exec.Cmd
	mu          sync.Mutex
	done        chan struct{}
	stopping    bool
	killTimeout time.Duration
	settleDelay time.Duration

	// Exited receives one event per unexpected child exit. It is buffered so a
	// caller that is busy rebuilding never blocks the wait goroutine.
	Exited chan ExitEvent
}

func New(logger *slog.Logger, killTimeout, settleDelay time.Duration) *Runner {
	return &Runner{
		logger:      logger,
		killTimeout: killTimeout,
		settleDelay: settleDelay,
		Exited:      make(chan ExitEvent, 1),
	}
}

func (r *Runner) Start(dir, execCmd string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.cmd != nil {
		return fmt.Errorf("process already running")
	}

	r.logger.Info("starting process...")

	// Create OS-aware command (direct exec on Windows, sh with ProcessGroups on Unix)
	cmd, err := createCommand(dir, execCmd)
	if err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start process: %w", err)
	}

	// Drop any exit event left over from a previous process so the caller does
	// not attribute it to this one.
	select {
	case <-r.Exited:
	default:
	}

	started := time.Now()
	r.cmd = cmd
	r.stopping = false
	r.done = make(chan struct{})
	done := r.done

	go func() {
		err := cmd.Wait()
		uptime := time.Since(started)
		close(done)

		r.mu.Lock()
		intentional := r.stopping
		if r.cmd == cmd {
			r.cmd = nil
		}
		r.mu.Unlock()

		if err != nil {
			r.logger.Warn("process exited", slog.Any("error", err), slog.Duration("uptime", uptime))
		} else {
			r.logger.Info("process exited cleanly", slog.Duration("uptime", uptime))
		}

		if !intentional {
			select {
			case r.Exited <- ExitEvent{Err: err, Uptime: uptime}:
			default:
			}
		}
	}()

	return nil
}

func (r *Runner) Stop() error {
	r.mu.Lock()
	cmd := r.cmd
	done := r.done
	if cmd != nil {
		// Mark the shutdown as ours so the wait goroutine does not report the
		// exit as a crash.
		r.stopping = true
	}
	r.mu.Unlock()

	if cmd == nil || cmd.Process == nil {
		return nil
	}

	r.logger.Info("stopping process...")

	// Use OS-specific kill logic (taskkill on Windows, syscall.Kill on Unix)
	if err := killProcess(cmd); err != nil {
		r.logger.Debug("kill signal failed", slog.Any("error", err))
	}

	// Wait for process to exit gracefully
	if done != nil {
		select {
		case <-done:
			// Process exited gracefully
		case <-time.After(r.killTimeout):
			r.logger.Warn("timeout waiting for process to exit; forcefully killing stubborn processes...",
				slog.Duration("kill_timeout", r.killTimeout))
			forceKillProcess(cmd) // Fallback to forceful OS-specific kill

			// Block until the forceful kill is processed by the OS and the wait goroutine closes the channel
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				r.logger.Error("process refused to die even after forceful kill")
			}
		}
	}

	// Brief pause to allow OS to release bound sockets (TIME_WAIT state) before proceeding
	if r.settleDelay > 0 {
		time.Sleep(r.settleDelay)
	}

	return nil
}
