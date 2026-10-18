// Package events implements GitHost's internal event bus. Events are
// emitted by the transports (push), the web handlers (issues, PRs) and
// the services (merges); consumers (webhooks, audit) subscribe by kind.
//
// The design is deliberately minimal: synchronous in-process delivery
// (personal server scale — no Kafka here). Consumers that need async
// behavior (webhook HTTP delivery with retries) read the event and
// schedule their own work.
package events

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
)

// Kind identifies what happened.
type Kind string

const (
	KindPush             Kind = "push"
	KindIssueOpened      Kind = "issue.opened"
	KindIssueClosed      Kind = "issue.closed"
	KindPROpened         Kind = "pr.opened"
	KindPRMerged         Kind = "pr.merged"
	KindPRReviewed       Kind = "pr.reviewed"
	KindReleasePublished Kind = "release.published"
)

// Event is one thing that happened on the instance.
type Event struct {
	Kind       Kind
	Repository string // "owner/name"
	Actor      string // username
	Detail     string // kind-specific JSON payload
}

// Handler processes one event. Errors are logged, not propagated —
// a misbehaving consumer must not break the emitter.
type Handler func(ctx context.Context, ev Event) error

// Bus dispatches events to registered handlers.
type Bus struct {
	mu       sync.RWMutex
	handlers map[Kind][]Handler
	logger   *slog.Logger
}

// New builds an event bus.
func New(logger *slog.Logger) *Bus {
	if logger == nil {
		logger = slog.Default()
	}
	return &Bus{handlers: make(map[Kind][]Handler), logger: logger}
}

// Subscribe registers a handler for a kind.
func (b *Bus) Subscribe(kind Kind, h Handler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers[kind] = append(b.handlers[kind], h)
}

// Emit dispatches an event to all registered handlers for its kind.
// Errors are logged; delivery is best-effort.
func (b *Bus) Emit(ctx context.Context, ev Event) {
	b.mu.RLock()
	handlers := b.handlers[ev.Kind]
	b.mu.RUnlock()

	for _, h := range handlers {
		if err := h(ctx, ev); err != nil {
			b.logger.Error("event_handler_failed",
				"kind", string(ev.Kind),
				"repository", ev.Repository,
				"err", err)
		}
	}
}

// String gives the event a human-readable form for logging.
func (e Event) String() string {
	return fmt.Sprintf("%s %s by %s", e.Kind, e.Repository, e.Actor)
}
