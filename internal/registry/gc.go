package registry

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/distribution/distribution/v3/registry/storage"
	"github.com/distribution/distribution/v3/registry/storage/driver"
)

var (
	ErrGCRunning = errors.New("garbage collection already running")

	// gcDrainTimeout is how long GC waits for running writes to finish before giving up.
	gcDrainTimeout = 60 * time.Second
	// gcAfterClose, when set, runs once the write gate is closed (tests).
	gcAfterClose func()
)

type GCState string

const (
	GCIdle    GCState = "idle"
	GCRunning GCState = "running"
	GCDone    GCState = "done"
	GCAborted GCState = "aborted"
)

// GCResult is what a finished collection removed: blob files (manifests are blobs too).
type GCResult struct {
	Removed    int
	BytesFreed int64
	Duration   time.Duration
}

// GCStatus is the state of the latest (or running) collection.
type GCStatus struct {
	State   GCState
	Result  GCResult
	Err     string
	Started time.Time
}

type gcState struct {
	mu sync.Mutex
	st GCStatus
}

func (r *Registry) GCStatus() GCStatus {
	r.gc.mu.Lock()
	defer r.gc.mu.Unlock()
	st := r.gc.st
	if st.State == "" {
		st.State = GCIdle
	}
	return st
}

func (r *Registry) gcSet(f func(*GCStatus)) {
	r.gc.mu.Lock()
	f(&r.gc.st)
	r.gc.mu.Unlock()
}

// blobStats counts the blob data files under the registry's blob store.
func blobStats(ctx context.Context, d driver.StorageDriver) (n int, bytes int64, err error) {
	err = d.Walk(ctx, "/docker/registry/v2/blobs", func(fi driver.FileInfo) error {
		if !fi.IsDir() {
			n++
			bytes += fi.Size()
		}
		return nil
	})
	var nf driver.PathNotFoundError
	if errors.As(err, &nf) {
		err = nil
	}
	return
}

// GC deletes untagged manifests and unreferenced blobs. Writes are refused while it runs;
// if running writes do not finish within gcDrainTimeout it aborts having deleted nothing.
// Pulls continue throughout. One collection at a time.
func (r *Registry) GC(ctx context.Context) (GCResult, error) {
	r.gc.mu.Lock()
	if r.gc.st.State == GCRunning {
		r.gc.mu.Unlock()
		return GCResult{}, ErrGCRunning
	}
	start := time.Now()
	r.gc.st = GCStatus{State: GCRunning, Started: start}
	r.gc.mu.Unlock()

	ctx = context.WithoutCancel(ctx) // an HTTP client going away must not stop a sweep
	fail := func(err error) (GCResult, error) {
		r.gcSet(func(s *GCStatus) { s.State, s.Err = GCAborted, err.Error() })
		return GCResult{}, err
	}
	if err := r.gate.Close(gcDrainTimeout); err != nil {
		r.gate.Open()
		return fail(err)
	}
	defer r.gate.Open()
	if gcAfterClose != nil {
		gcAfterClose()
	}
	if _, err := r.drv.Stat(ctx, "/docker/registry/v2/repositories"); err != nil {
		var nf driver.PathNotFoundError
		if errors.As(err, &nf) { // nothing was ever pushed: nothing to collect
			res := GCResult{Duration: time.Since(start)}
			r.gcSet(func(s *GCStatus) { s.State, s.Result = GCDone, res })
			return res, nil
		}
		return fail(err)
	}
	n0, b0, err := blobStats(ctx, r.drv)
	if err != nil {
		return fail(err)
	}
	if err := storage.MarkAndSweep(ctx, r.drv, r.ns, storage.GCOpts{RemoveUntagged: true, Quiet: true}); err != nil {
		return fail(err)
	}
	n1, b1, err := blobStats(ctx, r.drv)
	if err != nil {
		return fail(err)
	}
	res := GCResult{Removed: n0 - n1, BytesFreed: b0 - b1, Duration: time.Since(start)}
	r.gcSet(func(s *GCStatus) { s.State, s.Result = GCDone, res })
	return res, nil
}
