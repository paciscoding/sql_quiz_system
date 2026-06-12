package services

import "sync"

// Hub is an in-process pub/sub hub keyed by exam ID. It holds a set of
// subscriber channels, one per open SSE connection. All methods are safe for
// concurrent use.
type Hub struct {
	mu   sync.RWMutex
	subs map[int]map[chan string]struct{}
}

// NewHub creates a new, empty Hub.
func NewHub() *Hub {
	return &Hub{
		subs: make(map[int]map[chan string]struct{}),
	}
}

// Subscribe registers a new subscriber channel for the given exam ID.
// The returned channel is buffered (capacity 4) and will receive payloads
// sent by Broadcast. The caller is responsible for calling Unsubscribe
// when done (typically via defer).
func (h *Hub) Subscribe(examID int) chan string {
	ch := make(chan string, 4)
	h.mu.Lock()
	if h.subs[examID] == nil {
		h.subs[examID] = make(map[chan string]struct{})
	}
	h.subs[examID][ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

// Unsubscribe removes the channel from the exam's subscriber set and closes it.
// The close happens under the write lock so it cannot race with a concurrent
// Broadcast (which holds the read lock for the duration of its send loop),
// eliminating any send-on-closed-channel panic. The membership check makes the
// close idempotent against a prior Close().
func (h *Hub) Unsubscribe(examID int, ch chan string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if m, ok := h.subs[examID]; ok {
		if _, exists := m[ch]; exists {
			delete(m, ch)
			close(ch)
		}
		if len(m) == 0 {
			delete(h.subs, examID)
		}
	}
}

// Broadcast sends payload to all subscribers of the given exam ID.
// The read lock is held for the whole send loop so Unsubscribe/Close (which
// take the write lock before closing channels) cannot close a channel mid-send.
// Sends are non-blocking, so holding the lock never stalls on a slow consumer.
func (h *Hub) Broadcast(examID int, payload string) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.subs[examID] {
		select {
		case ch <- payload:
		default:
			// Skip full channels; client will get the next event.
		}
	}
}

// Close shuts down the hub by closing all subscriber channels and clearing
// the internal map. Should be called on graceful shutdown (e.g. via defer).
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for examID, m := range h.subs {
		for ch := range m {
			close(ch)
		}
		delete(h.subs, examID)
	}
}
