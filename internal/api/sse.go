package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// hub is a minimal server-sent-events broadcaster.
type hub struct {
	mu      sync.RWMutex
	clients map[chan string]struct{}
}

var sse = &hub{clients: map[chan string]struct{}{}}

func (h *hub) add() chan string {
	ch := make(chan string, 32)
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *hub) remove(ch chan string) {
	h.mu.Lock()
	if _, ok := h.clients[ch]; ok {
		delete(h.clients, ch)
		close(ch)
	}
	h.mu.Unlock()
}

// Broadcast pushes one event to every connected client (non-blocking).
func Broadcast(event string, data any) {
	b, err := json.Marshal(data)
	if err != nil {
		return
	}
	payload := fmt.Sprintf("event: %s\ndata: %s\n\n", event, b)
	sse.mu.RLock()
	defer sse.mu.RUnlock()
	for ch := range sse.clients {
		select {
		case ch <- payload:
		default: // slow client: drop the frame instead of blocking the scheduler
		}
	}
}

func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	if s.authRequired() && !s.loggedIn(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if _, err := fmt.Fprint(w, ": connected\n\n"); err != nil {
		return
	}
	fl.Flush()

	ch := sse.add()
	defer sse.remove(ch)

	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, open := <-ch:
			if !open {
				return
			}
			if _, err := fmt.Fprint(w, msg); err != nil {
				return
			}
			fl.Flush()
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			fl.Flush()
		}
	}
}
