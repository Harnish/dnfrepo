package access

import (
	"net/netip"
	"sync"
	"time"
)

const (
	maxFailures = 10
	window      = 5 * time.Minute
)

// throttle blocks an IP for `window` after maxFailures bad Basic-auth attempts within `window`.
type throttle struct {
	mu  sync.Mutex
	now func() time.Time
	m   map[netip.Addr]*entry
}

type entry struct {
	count        int
	start        time.Time
	blockedUntil time.Time
}

func newThrottle() *throttle { return &throttle{now: time.Now, m: map[netip.Addr]*entry{}} }

func (t *throttle) blocked(ip netip.Addr) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.m[ip]
	return e != nil && t.now().Before(e.blockedUntil)
}

func (t *throttle) fail(ip netip.Addr) {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := t.now()
	e := t.m[ip]
	if e == nil || n.Sub(e.start) > window {
		e = &entry{start: n}
		t.m[ip] = e
	}
	e.count++
	if e.count >= maxFailures {
		e.blockedUntil = n.Add(window)
	}
	if len(t.m) > 10000 { // bound memory: drop expired entries
		for k, v := range t.m {
			if n.Sub(v.start) > window && n.After(v.blockedUntil) {
				delete(t.m, k)
			}
		}
	}
}
