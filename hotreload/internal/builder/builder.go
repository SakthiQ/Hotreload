package builder

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

type Builder struct {
	logger *slog.Logger
}

func New(logger *slog.Logger) *Builder {
	return &Builder{
		logger: logger,
	}
}

func (b *Builder) Build(ctx context.Context, dir, buildCmd string) error {
	b.logger.Info("starting build...")
	start := time.Now()

	if strings.TrimSpace(buildCmd) == "" {
		return fmt.Errorf("build command is empty")
	}

	// The build command runs through a shell so that pipes, redirects and `&&`
	// work. Unlike the exec command there is no need to bypass the shell here:
	// the build is expected to finish, and cmd.Wait blocking on the wrapper is
	// exactly what we want.
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/c", buildCmd)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", buildCmd)
	}

	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err := cmd.Run()
	if ctx.Err() != nil {
		return fmt.Errorf("build canceled")
	}

	if err != nil {
		return fmt.Errorf("build failed: %w", err)
	}

	b.logger.Info("build successful", slog.Duration("duration", time.Since(start)))
	return nil
}
