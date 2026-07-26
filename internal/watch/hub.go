package watch

import "sync"

// Hub fans a change signal out to all subscribed SSE clients. Channels are
// buffered(1) with non-blocking sends, so a slow client coalesces refreshes
// instead of blocking the broadcaster.
type Hub struct {
	mu   sync.Mutex
	subs map[chan struct{}]struct{}
}

func NewHub() *Hub {
	return &Hub{subs: make(map[chan struct{}]struct{})}
}

func (h *Hub) Subscribe() (ch chan struct{}, cancel func()) {
	ch = make(chan struct{}, 1)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs, ch)
		h.mu.Unlock()
	}
}

func (h *Hub) Broadcast() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
