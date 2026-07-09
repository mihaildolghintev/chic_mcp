package telegram

import (
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
)

// secretTokenHeader is the header Telegram echoes back on every webhook
// delivery when a secret_token was passed to setWebhook.
const secretTokenHeader = "X-Telegram-Bot-Api-Secret-Token"

// Webhook is the HTTP receiver for Telegram updates. It verifies the secret
// token, deduplicates by update_id and hands updates to a buffered queue —
// always answering 200 immediately so Telegram never times out and re-delivers
// just because a reply is slow. Actual processing happens in worker goroutines
// (see RunWorkers).
type Webhook struct {
	secret  string
	updates chan Update
	seen    *dedupe
	logger  *slog.Logger
}

// NewWebhook builds a receiver. queueSize bounds the in-flight update buffer;
// beyond it updates are dropped (logged) rather than making Telegram wait.
func NewWebhook(secret string, queueSize int, logger *slog.Logger) *Webhook {
	if logger == nil {
		logger = slog.Default()
	}
	return &Webhook{
		secret:  secret,
		updates: make(chan Update, queueSize),
		seen:    newDedupe(1024),
		logger:  logger,
	}
}

// Updates is the queue consumed by worker goroutines.
func (w *Webhook) Updates() <-chan Update { return w.updates }

// ServeHTTP implements the webhook endpoint. The response is always fast: 401
// for a bad secret, 200 for everything else — even a full queue or malformed
// body. Retrying is pointless for those, so we never ask Telegram to.
func (w *Webhook) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	got := r.Header.Get(secretTokenHeader)
	if subtle.ConstantTimeCompare([]byte(got), []byte(w.secret)) != 1 {
		w.logger.Warn("webhook: bad secret token", "remote", r.RemoteAddr)
		http.Error(rw, "unauthorized", http.StatusUnauthorized)
		return
	}

	var u Update
	if err := json.NewDecoder(r.Body).Decode(&u); err != nil {
		w.logger.Warn("webhook: undecodable update", "err", err)
		rw.WriteHeader(http.StatusOK)
		return
	}
	if !w.seen.firstSeen(u.UpdateID) {
		w.logger.Debug("webhook: duplicate update dropped", "update_id", u.UpdateID)
		rw.WriteHeader(http.StatusOK)
		return
	}

	select {
	case w.updates <- u:
	default:
		w.logger.Error("webhook: queue full, dropping update", "update_id", u.UpdateID)
	}
	rw.WriteHeader(http.StatusOK)
}

// dedupe remembers the last n update_ids: Telegram re-delivers an update if it
// doubts our 200 reached it, and processing one twice would double-reply.
type dedupe struct {
	mu   sync.Mutex
	seen map[int64]struct{}
	ring []int64
	next int
}

func newDedupe(n int) *dedupe {
	return &dedupe{
		seen: make(map[int64]struct{}, n),
		ring: make([]int64, n),
	}
}

// firstSeen records id and reports whether this is its first appearance. The
// fixed-size ring evicts the oldest entry so memory stays bounded.
func (d *dedupe) firstSeen(id int64) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, dup := d.seen[id]; dup {
		return false
	}
	if old := d.ring[d.next]; old != 0 {
		delete(d.seen, old)
	}
	d.ring[d.next] = id
	d.next = (d.next + 1) % len(d.ring)
	d.seen[id] = struct{}{}
	return true
}
