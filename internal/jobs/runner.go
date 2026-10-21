// Package jobs runs scheduled maintenance tasks. The design is a
// simple poller: the server process checks for pending jobs every
// minute and claims them atomically (single-process + SQLite = the
// database IS the queue). Jobs are enqueued by the CLI and by the
// server itself (session pruning).
//
// The server holds the exclusive instance lock, so any job found in
// the running state at startup is a stale leftover from a crashed or
// killed previous run; the runner requeues it before polling.
package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"githost.dev/githost/internal/store"
)

// Runner polls the jobs table and executes pending tasks.
type Runner struct {
	store  *store.Store
	logger *slog.Logger
	stop   chan struct{}
	once   sync.Once
	done   sync.WaitGroup
	// housekeeping runs on every tick (e.g. in-memory limiter pruning).
	housekeeping []func()
}

// New builds a job runner.
func New(st *store.Store, logger *slog.Logger) *Runner {
	if logger == nil {
		logger = slog.Default()
	}
	return &Runner{store: st, logger: logger, stop: make(chan struct{})}
}

// RegisterHousekeeping adds a callback executed on every tick. Intended
// for small periodic chores that own no state of their own (such as
// pruning the login rate limiter's key map).
func (r *Runner) RegisterHousekeeping(fn func()) {
	if fn != nil {
		r.housekeeping = append(r.housekeeping, fn)
	}
}

// Start requeues stale running jobs and begins the poll loop in a
// goroutine. The context and Stop both terminate the loop; Stop also
// waits for the in-flight tick to finish.
func (r *Runner) Start(ctx context.Context) {
	r.done.Add(1)
	go func() {
		defer r.done.Done()
		r.requeueStale(ctx)
		r.loop(ctx)
	}()
}

// Stop terminates the poll loop and waits for the in-flight tick.
// Idempotent; safe to call even when Start was never called.
func (r *Runner) Stop() {
	r.once.Do(func() { close(r.stop) })
	r.done.Wait()
}

// requeueStale moves jobs stuck in running (from a previous process
// that died mid-tick) back to pending.
func (r *Runner) requeueStale(ctx context.Context) {
	n, err := r.store.RequeueStaleRunningJobs(ctx)
	if err != nil {
		r.logger.Error("job_requeue_failed", "err", err)
		return
	}
	if n > 0 {
		r.logger.Warn("stale_jobs_requeued", "count", n)
	}
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
			for _, fn := range r.housekeeping {
				fn()
			}
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

	if err := r.store.FinishJob(ctx, job.ID, runErr != nil, errorDetail(runErr)); err != nil {
		// An unfinished row would linger as running forever; the next
		// startup requeue would resurrect it, but surface the failure now.
		r.logger.Error("job_finish_failed", "kind", job.Kind, "id", job.ID, "err", err)
	}
	if runErr != nil {
		r.logger.Error("job_failed", "kind", job.Kind, "err", runErr)
	}
}

func errorDetail(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
