// Package realtime is a tiny in-memory pub/sub hub that fans group events
// out to live subscribers (SSE streams and WebTransport datagram sessions).
package realtime

import "sync"

// Event is a single broadcastable update for a group.
type Event struct {
	GroupID string `json:"group_id"`
	Kind    string `json:"kind"` // e.g. "expense", "settlement", "member"
	Message string `json:"message"`
}

// Hub manages subscribers keyed by group ID.
type Hub struct {
	mu   sync.RWMutex
	subs map[string]map[chan Event]struct{}
}

// NewHub creates an empty hub.
func NewHub() *Hub {
	return &Hub{subs: make(map[string]map[chan Event]struct{})}
}

// Subscribe registers a channel for a group and returns an unsubscribe func.
// The channel is buffered so a slow client never blocks the publisher.
func (h *Hub) Subscribe(groupID string) (<-chan Event, func()) {
	ch := make(chan Event, 16)
	h.mu.Lock()
	if h.subs[groupID] == nil {
		h.subs[groupID] = make(map[chan Event]struct{})
	}
	h.subs[groupID][ch] = struct{}{}
	h.mu.Unlock()

	return ch, func() {
		h.mu.Lock()
		if set, ok := h.subs[groupID]; ok {
			delete(set, ch)
			if len(set) == 0 {
				delete(h.subs, groupID)
			}
		}
		h.mu.Unlock()
		close(ch)
	}
}

// Publish broadcasts an event to every subscriber of its group.
// Drops the event for any subscriber whose buffer is full (best-effort).
func (h *Hub) Publish(e Event) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.subs[e.GroupID] {
		select {
		case ch <- e:
		default: // slow consumer: skip rather than stall the hub
		}
	}
}
