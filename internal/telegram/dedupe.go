package telegram

import "sync"

// dedupe remembers the last n update_ids. The fixed-size ring evicts the
// oldest entry so memory stays bounded.
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

// firstSeen records id and reports whether this is its first appearance.
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
