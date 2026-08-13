package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), DefaultFile)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	return path
}

func TestLoadMissingFileKeepsDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope.toml")

	cfg, found, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if found {
		t.Error("found = true for a file that does not exist")
	}
	if !reflect.DeepEqual(cfg, Default()) {
		t.Errorf("cfg = %+v, want defaults %+v", cfg, Default())
	}
}

func TestLoadFullFile(t *testing.T) {
	path := writeConfig(t, `
# a comment
root = "./testserver"
build = "go build -o server.exe ."   # trailing comment
exec = "./server.exe --port 8080"
exclude = ["docs", "testdata"]
include_ext = ["go", "html"]
debounce = "50ms"
kill_timeout = "3s"
settle_delay = 100
log_level = "debug"
`)

	cfg, found, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if !found {
		t.Error("found = false for an existing file")
	}

	want := Config{
		Root:        "./testserver",
		Build:       "go build -o server.exe .",
		Exec:        "./server.exe --port 8080",
		Exclude:     []string{"docs", "testdata"},
		IncludeExt:  []string{"go", "html"},
		Debounce:    50 * time.Millisecond,
		KillTimeout: 3 * time.Second,
		SettleDelay: 100 * time.Millisecond,
		LogLevel:    "debug",
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("cfg = %+v, want %+v", cfg, want)
	}
}

func TestLoadUnsetKeysKeepDefaults(t *testing.T) {
	path := writeConfig(t, "root = \".\"\n")

	cfg, _, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Debounce != Default().Debounce {
		t.Errorf("Debounce = %v, want the default %v", cfg.Debounce, Default().Debounce)
	}
	if !reflect.DeepEqual(cfg.IncludeExt, Default().IncludeExt) {
		t.Errorf("IncludeExt = %v, want the default %v", cfg.IncludeExt, Default().IncludeExt)
	}
}

func TestLoadCommaSeparatedList(t *testing.T) {
	path := writeConfig(t, `exclude = "docs, testdata"`)

	cfg, _, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if want := []string{"docs", "testdata"}; !reflect.DeepEqual(cfg.Exclude, want) {
		t.Errorf("Exclude = %v, want %v", cfg.Exclude, want)
	}
}

func TestLoadErrors(t *testing.T) {
	tests := map[string]string{
		"unknown key":        `frequency = "10s"`,
		"missing equals":     `root "."`,
		"bad duration":       `debounce = "soon"`,
		"bad log level":      `log_level = "loud"`,
		"table":              "[watch]\nroot = \".\"",
		"unterminated array": `exclude = ["a", "b"`,
	}

	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			if _, _, err := Load(writeConfig(t, body)); err == nil {
				t.Errorf("Load(%q) succeeded, want an error", body)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	valid := Default()
	valid.Root, valid.Build, valid.Exec = ".", "go build .", "./app"

	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate on a complete config returned %v", err)
	}

	missingExec := valid
	missingExec.Exec = ""
	if err := missingExec.Validate(); err == nil {
		t.Error("Validate accepted a config with no exec command")
	}

	badTimeout := valid
	badTimeout.KillTimeout = 0
	if err := badTimeout.Validate(); err == nil {
		t.Error("Validate accepted a zero kill timeout")
	}
}

func TestWatchesEverything(t *testing.T) {
	tests := []struct {
		exts []string
		want bool
	}{
		{nil, true},
		{[]string{"*"}, true},
		{[]string{"go", "*"}, true},
		{[]string{"go"}, false},
	}

	for _, tt := range tests {
		cfg := Config{IncludeExt: tt.exts}
		if got := cfg.WatchesEverything(); got != tt.want {
			t.Errorf("WatchesEverything(%v) = %v, want %v", tt.exts, got, tt.want)
		}
	}
}

func TestExampleConfigParses(t *testing.T) {
	cfg, found, err := Load(writeConfig(t, Example))
	if err != nil {
		t.Fatalf("the example written by `hotreload init` does not parse: %v", err)
	}
	if !found {
		t.Fatal("found = false")
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("the example config is not valid: %v", err)
	}
}
