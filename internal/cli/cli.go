// Package cli implements the githost command-line interface:
//
//	githost init    -- initialize an instance
//	githost serve   -- run the server
//	githost version -- print build metadata
//	githost doctor  -- check the instance and environment
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"githost.dev/githost/internal/app"
	"githost.dev/githost/internal/buildinfo"
	"githost.dev/githost/internal/config"
	"githost.dev/githost/internal/database"
	"githost.dev/githost/internal/git"
	"githost.dev/githost/internal/lock"
	"githost.dev/githost/internal/logging"
	"githost.dev/githost/migrations"
)

// Run executes the CLI and returns the process exit code: 0 on success,
// 2 on usage errors, 1 on runtime failures.
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "init":
		return runInit(ctx, rest, stdout, stderr)
	case "serve":
		return runServe(ctx, rest, stdout, stderr)
	case "version":
		return runVersion(rest, stdout, stderr)
	case "doctor":
		return runDoctor(ctx, rest, stdout, stderr)
	case "admin":
		return runAdmin(ctx, rest, stdin, stdout, stderr)
	case "repo":
		return runRepo(ctx, rest, stdout, stderr)
	case "backup":
		return runBackup(ctx, rest, stdin, stdout, stderr)
	case "help", "--help", "-h":
		usage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "githost: unknown command %q\n\n", cmd)
		usage(stderr)
		return 2
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `GitHost - a lightweight self-hosted Git platform

usage: githost <command> [flags]

Commands:
  init      Initialize a new GitHost instance (config, secret, database)
  serve     Start the GitHost server
  doctor    Check the instance and environment for problems
  version   Print version information
  admin     Manage users, tokens and SSH keys (run "githost admin help")
  repo      Manage repositories (run "githost repo help")
  backup    Backup and restore (run "githost backup help")
  help      Show this help

Run "githost <command> --help" for command-specific flags.
`)
}

// newFlagSet builds a flag set that reports errors to stderr with a concise
// usage message.
func newFlagSet(name, shortUsage string, stderr io.Writer) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		fmt.Fprintf(stderr, "usage: %s\n", shortUsage)
		flags.PrintDefaults()
	}
	return flags
}

func configFlag(flags *flag.FlagSet) *string {
	return flags.String("config", config.DefaultConfigPath, "path to the configuration file")
}

func runInit(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := newFlagSet("init", "githost init [--config FILE]", stderr)
	cfgPath := configFlag(flags)
	if err := flags.Parse(args); err != nil {
		return 2
	}

	created, err := config.EnsureFile(*cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "githost: %v\n", err)
		return 1
	}
	if created {
		fmt.Fprintf(stdout, "created configuration file %s\n", *cfgPath)
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "githost: %v\n", err)
		return 1
	}
	if err := app.Init(ctx, cfg); err != nil {
		fmt.Fprintf(stderr, "githost: init failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "GitHost instance initialized\n")
	fmt.Fprintf(stdout, "  data directory: %s\n", cfg.Paths.Data)
	fmt.Fprintf(stdout, "  database:       %s\n", cfg.Database.SQLite.Path)
	fmt.Fprintf(stdout, "  secret file:    %s\n", cfg.Security.SecretFile)
	fmt.Fprintln(stdout, `Run "githost serve" to start the server.`)
	return 0
}

func runServe(parent context.Context, args []string, stdout, stderr io.Writer) int {
	flags := newFlagSet("serve", "githost serve [--config FILE]", stderr)
	cfgPath := configFlag(flags)
	if err := flags.Parse(args); err != nil {
		return 2
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "githost: %v\n", err)
		if errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintln(stderr, `hint: run "githost init" first`)
		}
		return 1
	}
	logger, err := logging.New(cfg.Logging)
	if err != nil {
		fmt.Fprintf(stderr, "githost: %v\n", err)
		return 1
	}

	// Shut down cleanly on Ctrl+C (and SIGTERM where the OS has it).
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv, err := app.Start(ctx, cfg, logger)
	if err != nil {
		fmt.Fprintf(stderr, "githost: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "githost %s listening on http://%s\n", buildinfo.Version, srv.Addr())

	<-ctx.Done()
	logger.Info("shutdown_started")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Stop(shutdownCtx); err != nil {
		logger.Error("shutdown_failed", "err", err)
		return 1
	}
	logger.Info("shutdown_complete")
	return 0
}

func runVersion(args []string, stdout, stderr io.Writer) int {
	flags := newFlagSet("version", "githost version", stderr)
	if err := flags.Parse(args); err != nil {
		return 2
	}
	fmt.Fprintf(stdout, "githost version %s\n", buildinfo.Version)
	fmt.Fprintf(stdout, "  commit:     %s\n", buildinfo.Commit)
	fmt.Fprintf(stdout, "  build date: %s\n", buildinfo.Date)
	fmt.Fprintf(stdout, "  platform:   %s/%s\n", runtime.GOOS, runtime.GOARCH)
	return 0
}

// Doctor check statuses.
const (
	statusOK   = "ok"
	statusWarn = "warn"
	statusFail = "fail"
)

type checkResult struct {
	name   string
	status string
	detail string
}

func runDoctor(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := newFlagSet("doctor", "githost doctor [--config FILE]", stderr)
	cfgPath := configFlag(flags)
	if err := flags.Parse(args); err != nil {
		return 2
	}

	failed := false

	// 1. Configuration (everything else depends on it).
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintf(stdout, "[fail] configuration      %v\n", err)
		if errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintln(stdout, `       hint: run "githost init" first`)
		}
		return 1
	}
	results := []checkResult{{"configuration", statusOK, *cfgPath}}

	// 2. Data directory exists and is writable.
	probe := filepath.Join(cfg.Paths.Data, ".githost-doctor.tmp")
	if err := os.MkdirAll(cfg.Paths.Data, 0o755); err != nil {
		results = append(results, checkResult{"data directory", statusFail, err.Error()})
		failed = true
	} else if err := os.WriteFile(probe, []byte("ok"), 0o600); err != nil {
		results = append(results, checkResult{"data directory", statusFail, "not writable: " + err.Error()})
		failed = true
	} else {
		os.Remove(probe)
		results = append(results, checkResult{"data directory", statusOK, cfg.Paths.Data})
	}

	// 3. Instance secret.
	if _, err := os.Stat(cfg.Security.SecretFile); err == nil {
		results = append(results, checkResult{"instance secret", statusOK, cfg.Security.SecretFile})
	} else {
		results = append(results, checkResult{"instance secret", statusWarn, `missing (run "githost init")`})
	}

	// 4. Git executable (required from F1 for repository operations).
	if executor, err := git.New(nil); err != nil {
		results = append(results, checkResult{"git", statusWarn, err.Error()})
	} else if ver, err := executor.Version(ctx); err != nil {
		results = append(results, checkResult{"git", statusWarn, err.Error()})
	} else if !git.AtLeast(ver, 2, 30) {
		results = append(results, checkResult{"git", statusWarn, "version " + ver + " is older than the supported 2.30"})
	} else {
		results = append(results, checkResult{"git", statusOK, ver})
	}

	// 5. Database and migrations.
	if _, err := os.Stat(cfg.Database.SQLite.Path); errors.Is(err, os.ErrNotExist) {
		results = append(results, checkResult{"database", statusWarn, `not initialized (run "githost init")`})
	} else if err != nil {
		results = append(results, checkResult{"database", statusFail, err.Error()})
		failed = true
	} else {
		db, err := database.Open(ctx, cfg.Database.SQLite.Path)
		if err != nil {
			results = append(results, checkResult{"database", statusFail, err.Error()})
			failed = true
		} else {
			defer db.Close()
			applied, err := database.AppliedCount(ctx, db)
			if err != nil {
				results = append(results, checkResult{"database", statusFail, err.Error()})
				failed = true
			} else if total := countEmbeddedMigrations(); applied < total {
				results = append(results, checkResult{"database", statusWarn,
					fmt.Sprintf("%d of %d migrations applied (starting the server applies the rest)", applied, total)})
			} else {
				results = append(results, checkResult{"database", statusOK,
					fmt.Sprintf("%d migrations applied", applied)})
			}
		}
	}

	// 6. HTTP port availability (informational: an occupied port may just
	// mean the server is already running).
	addr := net.JoinHostPort(cfg.Server.Address, strconv.Itoa(cfg.Server.HTTPPort))
	if ln, err := net.Listen("tcp", addr); err == nil {
		ln.Close()
		results = append(results, checkResult{"http port", statusOK, addr + " available"})
	} else {
		results = append(results, checkResult{"http port", statusWarn, addr + " in use (is the server already running?)"})
	}

	// 7. Instance lock (is a server process running for this data dir?).
	if probe, err := lock.Acquire(filepath.Join(cfg.Paths.Data, "githost.lock")); err != nil {
		results = append(results, checkResult{"instance", statusOK, "running (lock held)"})
	} else {
		probe.Release()
		results = append(results, checkResult{"instance", statusOK, "not running (backup can be taken)"})
	}

	for _, r := range results {
		fmt.Fprintf(stdout, "[%-4s] %-18s %s\n", r.status, r.name, r.detail)
	}
	if failed {
		return 1
	}
	return 0
}

func countEmbeddedMigrations() int {
	migs, err := database.LoadMigrations(migrations.FS)
	if err != nil {
		return -1
	}
	return len(migs)
}
