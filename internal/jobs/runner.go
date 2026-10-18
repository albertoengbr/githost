// Package jobs runs scheduled maintenance tasks. The design is a
// simple poller: the server process checks for pending jobs every
// minute and claims them atomically (single-process + SQLite = the
// database IS the queue). Jobs are enqueued by the CLI and by the
// server itself (session pruning).
package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"githost.dev/githost/internal/store"
)

// Runner polls the jobs table and executes pending tasks.
type Runner struct {
	store  *store.Store
	logger *slog.Logger
	stop   chan struct{}
}

// New builds a job runner.
func New(st *store.Store, logger *slog.Logger) *Runner {
	if logger == nil {
		logger = slog.Default()
	}
	return &Runner{store: st, logger: logger, stop: make(chan struct{})}
}

// Start begins the poll loop in a goroutine.
func (r *Runner) Start(ctx context.Context) {
	go r.loop(ctx)
}

// Stop terminates the poll loop.
func (r *Runner) Stop() {
	close(r.stop)
}

func (r *Runner) loop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stop:
			return
		case <-ticker.C:
			r.tick(ctx)
		}
	}
}

// tick claims and runs one pending job.
func (r *Runner) tick(ctx context.Context) {
	job, err := r.store.ClaimNextPendingJob(ctx)
	if err == store.ErrNotFound {
		return
	}
	if err != nil {
		r.logger.Error("job_claim_failed", "err", err)
		return
	}

	r.logger.Info("job_running", "kind", job.Kind, "id", job.ID)

	var runErr error
	switch job.Kind {
	case "prune_sessions":
		n, err := r.store.PruneSessions(ctx)
		runErr = err
		if err == nil {
			r.logger.Info("job_prune_sessions", "removed", n)
		}
	case "rotate_backups":
		// Backup rotation is a no-op without a backup dir configured;
		// the CLI enqueues it with the dir path in the detail field.
		r.logger.Info("job_rotate_backups", "detail", job.Detail, "note", "run githost backup prune manually or via cron")
	default:
		runErr = fmt.Errorf("unknown job kind %q", job.Kind)
	}

	if runErr != nil {
		r.logger.Error("job_failed", "kind", job.Kind, "err", runErr)
		_ = r.store.FinishJob(ctx, job.ID, true, runErr.Error())
	} else {
		_ = r.store.FinishJob(ctx, job.ID, false, "")
	}
}
