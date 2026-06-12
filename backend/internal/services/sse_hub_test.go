package services

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// (a) Subscribe then Broadcast delivers the payload on the channel
// ---------------------------------------------------------------------------
func TestHub_SubscribeThenBroadcast(t *testing.T) {
	h := NewHub()
	defer h.Close()

	ch := h.Subscribe(1)
	h.Broadcast(1, "hello")

	select {
	case got := <-ch:
		if got != "hello" {
			t.Fatalf("expected %q got %q", "hello", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for broadcast payload")
	}
}

// ---------------------------------------------------------------------------
// (b) Broadcast to an exam with no subscribers is a no-op (must not panic)
// ---------------------------------------------------------------------------
func TestHub_BroadcastNoSubscribers(t *testing.T) {
	h := NewHub()
	defer h.Close()

	// Should not panic even though examID 99 has no subscribers
	h.Broadcast(99, "nobody home")
}

// ---------------------------------------------------------------------------
// (c) Full-buffer channel is skipped non-blocking; does not block other subs
// ---------------------------------------------------------------------------
func TestHub_FullBufferSkipped(t *testing.T) {
	h := NewHub()
	defer h.Close()

	// Subscribe two channels to the same exam
	slow := h.Subscribe(2)
	fast := h.Subscribe(2)

	// Fill the slow channel's buffer completely (cap 4)
	for i := 0; i < 4; i++ {
		slow <- "pre-fill"
	}

	// Broadcast must return without blocking and the fast channel gets the msg
	done := make(chan struct{})
	go func() {
		h.Broadcast(2, "new event")
		close(done)
	}()

	select {
	case <-done:
		// good — Broadcast returned
	case <-time.After(time.Second):
		t.Fatal("Broadcast blocked on full channel")
	}

	// fast received the new event
	select {
	case got := <-fast:
		if got != "new event" {
			t.Fatalf("fast channel got %q, want %q", got, "new event")
		}
	case <-time.After(time.Second):
		t.Fatal("fast channel did not receive the broadcast")
	}

	// slow still has its pre-filled messages, not the new one (was skipped)
	if len(slow) != 4 {
		t.Fatalf("slow channel len = %d, want 4 (skipped msg)", len(slow))
	}

	h.Unsubscribe(2, slow)
	h.Unsubscribe(2, fast)
}

// ---------------------------------------------------------------------------
// (d) Unsubscribe removes the channel and closes it
// ---------------------------------------------------------------------------
func TestHub_Unsubscribe(t *testing.T) {
	h := NewHub()

	ch := h.Subscribe(3)
	h.Unsubscribe(3, ch)

	// Channel should be closed now
	select {
	case _, open := <-ch:
		if open {
			t.Fatal("channel should be closed after Unsubscribe, but received a value")
		}
		// ok — closed channel reads zero value with open=false
	default:
		// Also acceptable — empty and closed; drain it
		_, open := <-ch
		if open {
			t.Fatal("channel was not closed after Unsubscribe")
		}
	}

	// A subsequent Broadcast must not panic (channel is gone from the hub)
	h.Broadcast(3, "after unsubscribe")
}

// ---------------------------------------------------------------------------
// (d-extra) Unsubscribe is idempotent — calling it twice must not double-close
// ---------------------------------------------------------------------------
func TestHub_UnsubscribeIdempotent(t *testing.T) {
	h := NewHub()

	ch := h.Subscribe(4)
	h.Unsubscribe(4, ch) // first call: normal
	// second call must not panic with "close of closed channel"
	h.Unsubscribe(4, ch)
}

// ---------------------------------------------------------------------------
// Close() closes all channels and a subsequent Unsubscribe is safe
// ---------------------------------------------------------------------------
func TestHub_CloseAndSubsequentUnsubscribe(t *testing.T) {
	h := NewHub()

	ch1 := h.Subscribe(5)
	ch2 := h.Subscribe(5)

	h.Close()

	// Both channels should be closed
	for _, ch := range []chan string{ch1, ch2} {
		select {
		case _, open := <-ch:
			if open {
				t.Fatal("expected closed channel after Hub.Close, received value")
			}
		default:
			_, open := <-ch
			if open {
				t.Fatal("expected closed channel after Hub.Close")
			}
		}
	}

	// Calling Unsubscribe on an already-closed hub must not panic
	h.Unsubscribe(5, ch1)
}

// ---------------------------------------------------------------------------
// (e) CONCURRENCY: many goroutines Subscribe / Broadcast / Unsubscribe
//     concurrently — must pass with -race, no send-on-closed-channel panic.
// ---------------------------------------------------------------------------
func TestHub_Concurrent_RaceDetector(t *testing.T) {
	h := NewHub()
	defer h.Close()

	const examID = 10
	const goroutines = 50
	const rounds = 20

	var wg sync.WaitGroup
	var panicCount atomic.Int64

	// Worker: subscribe, wait for one message or timeout, then unsubscribe
	subscriber := func() {
		defer wg.Done()
		defer func() {
			if r := recover(); r != nil {
				panicCount.Add(1)
				t.Errorf("panic in subscriber goroutine: %v", r)
			}
		}()
		for i := 0; i < rounds; i++ {
			ch := h.Subscribe(examID)
			// Short wait so broadcasts have a chance to land
			timer := time.NewTimer(2 * time.Millisecond)
			select {
			case <-ch:
			case <-timer.C:
			}
			timer.Stop()
			h.Unsubscribe(examID, ch)
		}
	}

	// Broadcaster: fires continuously while subscribers churn
	broadcaster := func() {
		defer wg.Done()
		defer func() {
			if r := recover(); r != nil {
				panicCount.Add(1)
				t.Errorf("panic in broadcaster goroutine: %v", r)
			}
		}()
		for i := 0; i < goroutines*rounds; i++ {
			h.Broadcast(examID, "event")
			// Yield to let other goroutines schedule
			time.Sleep(time.Microsecond)
		}
	}

	wg.Add(goroutines + 1)
	for i := 0; i < goroutines; i++ {
		go subscriber()
	}
	go broadcaster()

	wg.Wait()

	if panicCount.Load() != 0 {
		t.Fatalf("%d panics detected in concurrent test", panicCount.Load())
	}
}
