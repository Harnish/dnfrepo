package registry

import (
	"errors"
	"sync"
	"time"
)

var ErrDrainTimeout = errors.New("in-flight registry writes did not finish in time")

// Gate pauses registry writes (for garbage collection). Closing it rejects new writes at
// once and waits for the ones already running.
type Gate struct {
	mu     sync.Mutex
	closed bool
	n      int
	zero   chan struct{} // closed when n reaches 0 while a Close is waiting
}

// Begin reports whether a write may start; a true must be paired with End.
func (g *Gate) Begin() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return false
	}
	g.n++
	return true
}

func (g *Gate) End() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.n--
	if g.n == 0 && g.zero != nil {
		close(g.zero)
		g.zero = nil
	}
}

// Close rejects new writes and waits up to timeout for running ones. On timeout it returns
// ErrDrainTimeout and the gate stays closed (call Open to undo).
func (g *Gate) Close(timeout time.Duration) error {
	g.mu.Lock()
	g.closed = true
	if g.n == 0 {
		g.mu.Unlock()
		return nil
	}
	if g.zero == nil {
		g.zero = make(chan struct{})
	}
	ch := g.zero
	g.mu.Unlock()
	select {
	case <-ch:
		return nil
	case <-time.After(timeout):
		return ErrDrainTimeout
	}
}

func (g *Gate) Open() {
	g.mu.Lock()
	g.closed = false
	g.mu.Unlock()
}
