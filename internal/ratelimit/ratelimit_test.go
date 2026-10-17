package ratelimit

import (
	"sync"
	"testing"
	"time"
)

func TestAllowAndReset(t *testing.T) {
	l := New(3, 10*time.Minute)

	for i := 0; i < 3; i++ {
		if !l.Allow("ip:1") {
			t.Fatalf("event %d rejected inside budget", i+1)
		}
	}
	if l.Allow("ip:1") {
		t.Fatal("fourth event within window accepted")
	}
	// Other keys are unaffected.
	if !l.Allow("ip:2") {
		t.Fatal("second key affected by first key's budget")
	}

	l.Reset("ip:1")
	if !l.Allow("ip:1") {
		t.Fatal("reset did not clear the budget")
	}
}

func TestWindowSlides(t *testing.T) {
	l := New(1, 30*time.Millisecond)
	if !l.Allow("k") {
		t.Fatal("first event rejected")
	}
	if l.Allow("k") {
		t.Fatal("second event inside window accepted")
	}
	time.Sleep(60 * time.Millisecond)
	if !l.Allow("k") {
		t.Fatal("event after window rejected")
	}
}

func TestPruneAndConcurrency(t *testing.T) {
	l := New(100, 10*time.Millisecond)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l.Allow("k")
		}()
	}
	wg.Wait()
	time.Sleep(20 * time.Millisecond)
	l.Prune()
	l.mu.Lock()
	n := len(l.events)
	l.mu.Unlock()
	if n != 0 {
		t.Fatalf("prune left %d keys", n)
	}
}
