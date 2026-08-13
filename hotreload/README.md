# HotReload CLI

A file‑watching hot reload tool written in Go.  It watches a project
directory, rebuilds when source files change, and restarts the server
process automatically.  The implementation is intentionally small and
self‑contained; the only third‑party dependency is `fsnotify` for
filesystem events, and `log/slog` from the standard library is used for
logging.

This repository contains two main components:

* `hotreload/` – the CLI tool source code (`main.go` plus `internal/` packages).
* `testserver/` – a minimal HTTP server used for demonstration and testing.

There is also a `Makefile` which can build, test, and run a demo of the tool.

## Features

- Watch an entire directory tree (recursively) for changes, picking up
	directories created after startup.
- Ignore common noisy directories (`.git`, `node_modules`, `vendor`,
	build artifacts, hidden dirs) plus user‑specified excludes.
- Only rebuild for file types you care about (`include_ext`, `go` by
	default) so a README or log file edit does not restart the server.
- Debounce rapid file events (200 ms by default) to avoid unnecessary
	rebuilds.
- Run a build command on change and stream stdout/stderr in real time.
- Start/stop the server process cleanly; kill stubborn processes if
	necessary.
- Detect a server that crashes on startup and retry with exponential
	backoff, giving up after 5 attempts until the next file change — no
	restart loops.
- Configure everything from a config file, the command line, or both.
- Works on Windows and Unix; execs the binary directly on Windows so
	the process tree is tracked correctly, and uses process groups on Unix.

## Quickstart

### With a config file (recommended)

```bash
hotreload init      # writes .hotreload.toml
# edit root / build / exec, then:
hotreload
```

### With flags only

```bash
hotreload --root ./testserver \
	--build "go build -o server ." \
	--exec "./server"
```

On first run the tool immediately triggers a build and starts the
server.  Edit a `.go` file under the root and save; the watcher detects
the change, rebuilds, and restarts the binary.

Note that `--build` and `--exec` run with their working directory set to
`--root`, so paths in them are relative to the root, not to where you
launched `hotreload`.

### Building from source

```bash
cd hotreload
make build          # or: go build -o hotreload .
make check          # go vet + go test ./...
```

On Windows (PowerShell), without `make`:

```powershell
cd hotreload
go build -o hotreload.exe .
go test ./...
.\hotreload.exe --root ".\testserver" --build "go build -o server.exe ." --exec ".\server.exe"
```

Stop with Ctrl+C.

## Commands

```text
hotreload           run the watcher
hotreload init      write a .hotreload.toml with every setting documented
hotreload version   print the version and commit
```

## CLI Flags

```text
--config <path>       config file to read (default .hotreload.toml if present)
--root <path>         project root directory to watch (required)
--build <command>     build command to run on changes (required)
--exec <command>      command used to launch the built binary (required)
--exclude <dirs>      comma-separated relative paths to ignore
--include-ext <exts>  comma-separated extensions that trigger a rebuild,
                      or "*" for every file (default go,mod,sum)
--debounce <dur>      quiet period before rebuilding (default 200ms)
--kill-timeout <dur>  wait before force-killing the process (default 5s)
--settle-delay <dur>  pause after exit to let the OS release the port (default 500ms)
--log-level <level>   debug, info, warn or error (default info)
--version             print the version and exit
```

Settings are resolved as **defaults → config file → flags**: a flag you
pass always wins, and a flag you omit never overwrites the file.

## Configuration file

`hotreload` reads `.hotreload.toml` from the working directory, or the
path given to `--config`.  The format is a small subset of TOML —
comments and top-level `key = value` pairs:

```toml
root = "."
build = "go build -o ./tmp/app ."
exec = "./tmp/app"

exclude = ["testdata", "docs"]
include_ext = ["go", "mod", "sum"]

debounce = "200ms"
kill_timeout = "5s"
settle_delay = "500ms"
log_level = "info"
```

Durations accept Go duration strings (`"200ms"`, `"2s"`); a bare number
is read as milliseconds.  Lists accept either an array or a
comma-separated string.  Unknown keys are an error rather than a silent
typo.  Run `hotreload init` to generate this file with all the settings
documented inline.

## Crash handling

If the built binary exits within 2 seconds of starting, that is treated
as a crash on startup rather than a normal exit.  The tool retries with
an exponential backoff (1s, 2s, 4s, …, capped at 30s) and stops after 5
consecutive crashes, waiting for the next file change before trying
again.  A process that ran longer than 2 seconds and then exits is
reported and left alone — the next file change restarts it.

## Testing

```bash
cd hotreload
go test ./...
```

The suite covers the watcher's exclude/extension filtering and
debouncing, the config parser, quote-aware command splitting, the
builder's failure and cancellation paths, and the runner's crash
reporting.  The runner and builder tests spawn short-lived child
processes.

## Implementation Notes

- `internal/config` parses the config file and holds the defaults.
- `internal/watcher` wraps `fsnotify` and handles recursion, debouncing,
	and ignore/exclude/extension logic.
- `internal/builder` executes build commands with context cancellation.
- `internal/runner` starts/stops child processes, handles graceful and
	forceful termination, and reports unexpected exits.
- `internal/shellwords` splits a command line without a shell, so the
	Windows runner can exec the binary directly and still support
	arguments containing spaces.
- `internal/app` ties everything together with a main event loop.

This project was built from scratch and does **not** use existing
hot‑reload frameworks such as `air`, `realize`, or `reflex`.
