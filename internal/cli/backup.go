package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"githost.dev/githost/internal/backup"
	"githost.dev/githost/internal/config"
)

// backupsDir returns the conventional backups directory next to the
// configuration file.
func backupsDir(cfgPath string) (string, error) {
	abs, err := filepath.Abs(cfgPath)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(abs), "backups"), nil
}

func runBackup(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		backupUsage(stderr)
		return 2
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "create":
		return runBackupCreate(ctx, rest, stdout, stderr)
	case "validate":
		return runBackupValidate(rest, stdout, stderr)
	case "restore":
		return runBackupRestore(ctx, rest, stdout, stderr)
	case "list":
		return runBackupList(rest, stdout, stderr)
	case "prune":
		return runBackupPrune(rest, stdout, stderr)
	case "--help", "-h", "help":
		backupUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "githost backup: unknown subcommand %q\n\n", sub)
		backupUsage(stderr)
		return 2
	}
}

func backupUsage(w io.Writer) {
	fmt.Fprint(w, `usage: githost backup <subcommand> [flags]

Subcommands:
  create    Create a backup (the server must be stopped)
  validate  Verify a backup archive against its manifest
  restore   Restore a backup into a clean data directory
  list      List backups in a directory
  prune     Keep only the newest N backups
`)
}

// cliLogger logs to stderr so command output on stdout stays parseable.
func cliLogger(stderr io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(stderr, nil))
}

func runBackupCreate(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("backup create", "githost backup create [--config FILE] [--out FILE]", stderr)
	cfgPath := configFlag(fs)
	out := fs.String("out", "", "output archive (default: <config-dir>/backups/githost-backup-<timestamp>.tar.gz)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "githost: %v\n", err)
		return 1
	}
	if *out == "" {
		dir, err := backupsDir(*cfgPath)
		if err != nil {
			fmt.Fprintf(stderr, "githost: %v\n", err)
			return 1
		}
		*out = filepath.Join(dir, backup.DefaultFileName(time.Now()))
	}
	if err := backup.Create(ctx, backup.CreateOptions{
		DataDir:    cfg.Paths.Data,
		ConfigPath: *cfgPath,
		Out:        *out,
		Logger:     cliLogger(stderr),
	}); err != nil {
		fmt.Fprintf(stderr, "githost: backup failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "backup created: %s\n", *out)
	fmt.Fprintf(stdout, "verify it with: githost backup validate --archive %s\n", *out)
	return 0
}

func runBackupValidate(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("backup validate", "githost backup validate --archive FILE", stderr)
	archive := fs.String("archive", "", "backup archive to validate")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *archive == "" {
		fmt.Fprintln(stderr, "githost: --archive is required")
		return 2
	}
	manifest, err := backup.Validate(*archive)
	if err != nil {
		fmt.Fprintf(stderr, "githost: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "archive OK: %d files, created %s\n",
		len(manifest.Entries), manifest.CreatedAt)
	return 0
}

func runBackupRestore(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("backup restore", "githost backup restore --archive FILE --data DIR [--config-out FILE]", stderr)
	archive := fs.String("archive", "", "backup archive to restore")
	dataDir := fs.String("data", "", "target data directory (must be empty; use with a fresh config)")
	configOut := fs.String("config-out", "", "optional path to write the backed-up configuration file to")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *archive == "" || *dataDir == "" {
		fmt.Fprintln(stderr, "githost: --archive and --data are required")
		return 2
	}
	if err := backup.Restore(ctx, backup.RestoreOptions{
		Archive:   *archive,
		DataDir:   *dataDir,
		ConfigOut: *configOut,
		Logger:    cliLogger(stderr),
	}); err != nil {
		fmt.Fprintf(stderr, "githost: restore failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "backup restored into %s\n", *dataDir)
	if *configOut != "" {
		fmt.Fprintf(stdout, "configuration written to %s (edit paths before serving)\n", *configOut)
	}
	fmt.Fprintln(stdout, "next: point a fresh githost.yaml at the restored data directory and run \"githost serve\"")
	return 0
}

func runBackupList(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("backup list", "githost backup list [--config FILE]", stderr)
	cfgPath := configFlag(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	dir, err := backupsDir(*cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "githost: %v\n", err)
		return 1
	}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		fmt.Fprintf(stdout, "no backups in %s\n", dir)
		return 0
	}
	infos, err := backup.List(dir)
	if err != nil {
		fmt.Fprintf(stderr, "githost: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "NAME\tSIZE\tCREATED")
	for _, info := range infos {
		fmt.Fprintf(stdout, "%s\t%d\t%s\n", info.Name, info.Size, info.CreatedAt.UTC().Format(time.RFC3339))
	}
	return 0
}

func runBackupPrune(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("backup prune", "githost backup prune [--config FILE] --keep N", stderr)
	cfgPath := configFlag(fs)
	keep := fs.Int("keep", 7, "number of newest backups to keep")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	dir, err := backupsDir(*cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "githost: %v\n", err)
		return 1
	}
	removed, err := backup.Prune(dir, *keep)
	if err != nil {
		fmt.Fprintf(stderr, "githost: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "kept %d newest, removed %d backups\n", *keep, len(removed))
	return 0
}
