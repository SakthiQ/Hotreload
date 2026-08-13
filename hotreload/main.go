package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/sakthi-narayan/hotreload/internal/app"
	"github.com/sakthi-narayan/hotreload/internal/config"
	"github.com/sakthi-narayan/hotreload/internal/logger"
)

// Build metadata, overridable at link time:
//
//	go build -ldflags "-X main.version=v1.0.0 -X main.commit=$(git rev-parse --short HEAD)"
var (
	version = "dev"
	commit  = "none"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "hotreload:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	// Subcommands are checked before flag parsing so `hotreload init` does not
	// need the otherwise-required flags.
	if len(args) > 0 {
		switch args[0] {
		case "init":
			return writeConfigFile(config.DefaultFile)
		case "version":
			fmt.Printf("hotreload %s (%s)\n", version, commit)
			return nil
		}
	}

	fs := flag.NewFlagSet("hotreload", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: hotreload [flags]\n       hotreload init\n       hotreload version\n\n"+
			"Settings are read from %s in the working directory, if present,\nand any flag given overrides the file.\n\nflags:\n",
			config.DefaultFile)
		fs.PrintDefaults()
	}

	// The defaults shown in --help are the real ones. Which flags actually
	// take effect is decided by fs.Visit below, not by these values.
	def := config.Default()
	var (
		configPath  = fs.String("config", "", "path to a config file (default "+config.DefaultFile+" if it exists)")
		root        = fs.String("root", "", "project root directory to watch")
		buildCmd    = fs.String("build", "", "build command")
		execCmd     = fs.String("exec", "", "command used to launch the built binary")
		excludeStr  = fs.String("exclude", "", "comma-separated relative directories to exclude from watching")
		includeStr  = fs.String("include-ext", strings.Join(def.IncludeExt, ","), `comma-separated file extensions that trigger a rebuild, or "*" for all`)
		debounce    = fs.Duration("debounce", def.Debounce, "quiet period after a file event before rebuilding")
		killTimeout = fs.Duration("kill-timeout", def.KillTimeout, "how long to wait for the process to exit before killing it")
		settleDelay = fs.Duration("settle-delay", def.SettleDelay, "pause after the process exits, letting the OS release its port")
		logLevel    = fs.String("log-level", def.LogLevel, "debug, info, warn or error")
		showVersion = fs.Bool("version", false, "print the version and exit")
	)

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		// flag already reported the problem and printed the usage.
		return errors.New("invalid arguments")
	}

	if *showVersion {
		fmt.Printf("hotreload %s (%s)\n", version, commit)
		return nil
	}

	path := *configPath
	if path == "" {
		path = config.DefaultFile
	}
	cfg, found, err := config.Load(path)
	if err != nil {
		return err
	}
	if *configPath != "" && !found {
		return fmt.Errorf("config file not found: %s", *configPath)
	}

	// Flags win over the file, but only the ones actually given: an unset flag
	// must not overwrite a configured value with its zero value.
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "root":
			cfg.Root = *root
		case "build":
			cfg.Build = *buildCmd
		case "exec":
			cfg.Exec = *execCmd
		case "exclude":
			cfg.Exclude = config.SplitList(*excludeStr)
		case "include-ext":
			cfg.IncludeExt = config.SplitList(*includeStr)
		case "debounce":
			cfg.Debounce = *debounce
		case "kill-timeout":
			cfg.KillTimeout = *killTimeout
		case "settle-delay":
			cfg.SettleDelay = *settleDelay
		case "log-level":
			cfg.LogLevel = *logLevel
		}
	})

	if err := cfg.Validate(); err != nil {
		if !found {
			return fmt.Errorf("%w\n\nPass the flags directly or run `hotreload init` to create %s", err, config.DefaultFile)
		}
		return err
	}

	level, err := config.ParseLevel(cfg.LogLevel)
	if err != nil {
		return err
	}

	log := logger.NewLogger(level)
	log.Info("hotreload starting", "version", version)
	if found {
		log.Info("loaded config", "path", path)
	}
	if cfg.WatchesEverything() {
		log.Info("watching every file type")
	}

	a, err := app.New(log, cfg)
	if err != nil {
		return fmt.Errorf("failed to initialize: %w", err)
	}

	if err := a.Run(); err != nil {
		return err
	}

	log.Info("hotreload exit")
	return nil
}

// writeConfigFile scaffolds a config file, refusing to clobber an existing one.
func writeConfigFile(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("%s already exists; delete it first if you want a fresh one", path)
		}
		return err
	}
	defer f.Close()

	if _, err := f.WriteString(config.Example); err != nil {
		return err
	}

	fmt.Printf("wrote %s - edit the root, build and exec settings, then run: hotreload\n", path)
	return nil
}
