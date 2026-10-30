package access

import (
	"net/netip"
	"sync"
	"time"
)

const (
	maxFailures = 10
	window      = 5 * time.Minute
	maxTracked  = 10000 // hard cap on tracked clients
)

// Throttle blocks a client for `window` after more than maxFailures attempts within `window`.
// IPv6 clients are tracked per /64, since one host can rotate through a whole /64.
type Throttle struct {
	mu  sync.Mutex
	now func() time.Time
	m   map[netip.Addr]*entry
}

type entry struct {
	count        int
	start        time.Time
	blockedUntil time.Time
}

func NewThrottle() *Throttle { return &Throttle{now: time.Now, m: map[netip.Addr]*entry{}} }

func throttleKey(ip netip.Addr) netip.Addr {
	ip = ip.Unmap()
	if ip.Is6() {
		return netip.PrefixFrom(ip, 64).Masked().Addr()
	}
	return ip
}

// entryFor returns the live entry for key, starting a fresh one when absent or expired.
// Caller holds t.mu.
func (t *Throttle) entryFor(key netip.Addr, n time.Time) *entry {
	e := t.m[key]
	if e != nil && n.Sub(e.start) <= window {
		return e
	}
	if e == nil && len(t.m) >= maxTracked {
		t.makeRoom(n)
	}
	e = &entry{start: n}
	t.m[key] = e
	return e
}

// makeRoom drops expired entries, then, if still full, arbitrary unblocked ones.
// Blocked clients are kept so flooding with other addresses cannot unblock them.
func (t *Throttle) makeRoom(n time.Time) {
	for k, v := range t.m {
		if n.Sub(v.start) > window && !n.Before(v.blockedUntil) {
			delete(t.m, k)
		}
	}
	for k, v := range t.m {
		if len(t.m) <= maxTracked*9/10 { // evict in batches so a flood costs O(1) amortized
			return
		}
		if !n.Before(v.blockedUntil) {
			delete(t.m, k)
		}
	}
}

func (t *Throttle) Blocked(ip netip.Addr) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.m[throttleKey(ip)]
	return e != nil && t.now().Before(e.blockedUntil)
}

// Fail records a failed attempt.
func (t *Throttle) Fail(ip netip.Addr) {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := t.now()
	e := t.entryFor(throttleKey(ip), n)
	e.count++
	if e.count >= maxFailures {
		e.blockedUntil = n.Add(window)
	}
}

// Attempt counts an attempt before it is verified and reports whether it may proceed.
// Counting up front (not after the check) keeps a parallel burst from slipping past the limit.
// Call Reset when the attempt succeeds.
func (t *Throttle) Attempt(ip netip.Addr) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := t.now()
	key := throttleKey(ip)
	if e := t.m[key]; e != nil && n.Before(e.blockedUntil) {
		return false
	}
	e := t.entryFor(key, n)
	e.count++
	if e.count > maxFailures {
		e.blockedUntil = n.Add(window)
		return false
	}
	return true
}

// Reset forgets a client's attempts (after a successful login).
func (t *Throttle) Reset(ip netip.Addr) {
	t.mu.Lock()
	delete(t.m, throttleKey(ip))
	t.mu.Unlock()
}
