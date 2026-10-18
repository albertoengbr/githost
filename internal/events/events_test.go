package events

import (
	"context"
	"sync"
	"testing"
)

func TestBusSubscribeEmit(t *testing.T) {
	bus := New(nil)

	var mu sync.Mutex
	var got []Event
	bus.Subscribe(KindPush, func(_ context.Context, ev Event) error {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, ev)
		return nil
	})

	bus.Emit(context.Background(), Event{
		Kind: KindPush, Repository: "alice/proj", Actor: "alice", Detail: `{"ref":"refs/heads/main"}`,
	})

	if len(got) != 1 || got[0].Repository != "alice/proj" {
		t.Fatalf("got %+v", got)
	}
}

func TestBusHandlerErrorNotFatal(t *testing.T) {
	bus := New(nil)
	ok := false
	bus.Subscribe(KindPush, func(_ context.Context, _ Event) error {
		return context.DeadlineExceeded // handler error
	})
	bus.Subscribe(KindPush, func(_ context.Context, _ Event) error {
		ok = true
		return nil
	})

	bus.Emit(context.Background(), Event{Kind: KindPush, Repository: "x", Actor: "y"})
	if !ok {
		t.Fatal("second handler not called after first failed")
	}
}

func TestBusUnrelatedKind(t *testing.T) {
	bus := New(nil)
	called := false
	bus.Subscribe(KindIssueOpened, func(_ context.Context, _ Event) error {
		called = true
		return nil
	})
	bus.Emit(context.Background(), Event{Kind: KindPush, Repository: "x", Actor: "y"})
	if called {
		t.Fatal("wrong-kind handler was called")
	}
}
