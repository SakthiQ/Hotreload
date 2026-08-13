// Package config holds the hotreload settings and loads them from a file.
//
// The file format is a small subset of TOML: comments, and top-level
// `key = value` pairs where the value is a quoted string, a bare number, or an
// array of quoted strings. That is enough for every setting here and keeps
// fsnotify the only third-party dependency.
package config

import (
	"bufio"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

// ParseLevel maps a log_level setting onto a slog level.
func ParseLevel(name string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "debug":
		return slog.LevelDebug, nil
	case "", "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("invalid log level %q (want debug, info, warn or error)", name)
	}
}

// DefaultFile is the config file looked for in the working directory when
// --config is not given.
const DefaultFile = ".hotreload.toml"

// WatchAll is the IncludeExt value that disables extension filtering.
const WatchAll = "*"

type Config struct {
	Root        string
	Build       string
	Exec        string
	Exclude     []string
	IncludeExt  []string
	Debounce    time.Duration
	KillTimeout time.Duration
	SettleDelay time.Duration
	LogLevel    string
}

// Default returns the configuration used when nothing is specified. The
// IncludeExt default keeps a documentation or log file edit from triggering a
// rebuild; set it to "*" to watch every file, which was the behaviour before
// extension filtering existed.
func Default() Config {
	return Config{
		IncludeExt:  []string{"go", "mod", "sum"},
		Debounce:    200 * time.Millisecond,
		KillTimeout: 5 * time.Second,
		SettleDelay: 500 * time.Millisecond,
		LogLevel:    "info",
	}
}

// Validate reports whether the configuration is usable.
func (c Config) Validate() error {
	switch {
	case c.Root == "":
		return fmt.Errorf("missing required setting: root (--root)")
	case c.Build == "":
		return fmt.Errorf("missing required setting: build (--build)")
	case c.Exec == "":
		return fmt.Errorf("missing required setting: exec (--exec)")
	case c.Debounce < 0:
		return fmt.Errorf("debounce must not be negative")
	case c.KillTimeout <= 0:
		return fmt.Errorf("kill_timeout must be greater than zero")
	case c.SettleDelay < 0:
		return fmt.Errorf("settle_delay must not be negative")
	}
	if _, err := ParseLevel(c.LogLevel); err != nil {
		return err
	}
	return nil
}

// WatchesEverything reports whether extension filtering is disabled.
func (c Config) WatchesEverything() bool {
	if len(c.IncludeExt) == 0 {
		return true
	}
	for _, ext := range c.IncludeExt {
		if ext == WatchAll {
			return true
		}
	}
	return false
}

// Load reads a config file on top of Default. A missing file at the default
// path is not an error: found reports whether a file was actually read, so the
// caller can tell "no config" from "empty config".
func Load(path string) (cfg Config, found bool, err error) {
	cfg = Default()

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, false, nil
		}
		return cfg, false, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for line := 1; scanner.Scan(); line++ {
		if err := applyLine(&cfg, scanner.Text()); err != nil {
			return cfg, true, fmt.Errorf("%s:%d: %w", path, line, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return cfg, true, err
	}

	return cfg, true, nil
}

func applyLine(cfg *Config, raw string) error {
	line := strings.TrimSpace(raw)
	if line == "" || strings.HasPrefix(line, "#") {
		return nil
	}
	if strings.HasPrefix(line, "[") {
		return fmt.Errorf("tables are not supported; use top-level key = value pairs")
	}

	key, value, ok := strings.Cut(line, "=")
	if !ok {
		return fmt.Errorf("expected key = value, got %q", line)
	}
	key = strings.TrimSpace(key)
	value = stripComment(strings.TrimSpace(value))

	switch key {
	case "root":
		return assignString(&cfg.Root, key, value)
	case "build":
		return assignString(&cfg.Build, key, value)
	case "exec":
		return assignString(&cfg.Exec, key, value)
	case "log_level":
		if err := assignString(&cfg.LogLevel, key, value); err != nil {
			return err
		}
		_, err := ParseLevel(cfg.LogLevel)
		return err
	case "exclude":
		return assignList(&cfg.Exclude, key, value)
	case "include_ext":
		return assignList(&cfg.IncludeExt, key, value)
	case "debounce":
		return assignDuration(&cfg.Debounce, key, value)
	case "kill_timeout":
		return assignDuration(&cfg.KillTimeout, key, value)
	case "settle_delay":
		return assignDuration(&cfg.SettleDelay, key, value)
	default:
		return fmt.Errorf("unknown setting %q", key)
	}
}

// stripComment removes a trailing `#` comment that is not inside a quoted
// string, so `debounce = "200ms" # fast` parses.
func stripComment(value string) string {
	var quote rune
	for i, r := range value {
		switch {
		case quote != 0 && r == quote:
			quote = 0
		case quote == 0 && (r == '\'' || r == '"'):
			quote = r
		case quote == 0 && r == '#':
			return strings.TrimSpace(value[:i])
		}
	}
	return value
}

func assignString(dst *string, key, value string) error {
	s, err := unquote(value)
	if err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	*dst = s
	return nil
}

func assignDuration(dst *time.Duration, key, value string) error {
	s, err := unquote(value)
	if err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	// A bare number is read as milliseconds, which is what people mean by
	// `debounce = 200`.
	if n, numErr := strconv.Atoi(s); numErr == nil {
		*dst = time.Duration(n) * time.Millisecond
		return nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("%s: invalid duration %q (try \"200ms\" or \"2s\")", key, s)
	}
	*dst = d
	return nil
}

func assignList(dst *[]string, key, value string) error {
	if !strings.HasPrefix(value, "[") {
		// Accept a plain comma-separated string so the file and the CLI flag
		// take the same syntax.
		s, err := unquote(value)
		if err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		*dst = SplitList(s)
		return nil
	}
	if !strings.HasSuffix(value, "]") {
		return fmt.Errorf("%s: unterminated array (multi-line arrays are not supported)", key)
	}

	inner := strings.TrimSpace(value[1 : len(value)-1])
	if inner == "" {
		*dst = nil
		return nil
	}

	var items []string
	for _, item := range strings.Split(inner, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		s, err := unquote(item)
		if err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		if s != "" {
			items = append(items, s)
		}
	}
	*dst = items
	return nil
}

func unquote(value string) (string, error) {
	if len(value) >= 2 {
		first, last := value[0], value[len(value)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			return value[1 : len(value)-1], nil
		}
	}
	if strings.ContainsAny(value, `"'`) {
		return "", fmt.Errorf("mismatched quotes in %s", value)
	}
	if value == "" {
		return "", fmt.Errorf("missing value")
	}
	return value, nil
}

// SplitList parses a comma-separated flag value, dropping empty entries.
func SplitList(s string) []string {
	var out []string
	for _, item := range strings.Split(s, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// Example is the file written by `hotreload init`.
const Example = `# hotreload configuration
# Every setting here can be overridden by the matching command-line flag.

# Directory tree to watch (required).
root = "."

# Command run when a watched file changes (required).
build = "go build -o ./tmp/app ."

# Command used to launch the built binary (required).
exec = "./tmp/app"

# Directories to skip, relative to root. Hidden directories and the usual
# build-output directories are always skipped.
exclude = ["testdata", "docs"]

# File extensions that trigger a rebuild. Use ["*"] to watch every file.
include_ext = ["go", "mod", "sum"]

# How long to wait for the file events to stop before rebuilding.
debounce = "200ms"

# How long to wait for the child process to exit before killing it forcefully.
kill_timeout = "5s"

# Pause after the child exits, giving the OS time to release its listen socket.
settle_delay = "500ms"

# One of: debug, info, warn, error.
log_level = "info"
`
