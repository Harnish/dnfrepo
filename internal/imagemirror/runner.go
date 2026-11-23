package imagemirror

import (
	"context"
	"log"
	"os"
	"time"
)

// RunAll runs every mirror, logging failures and carrying on.
func (m *Mirrors) RunAll(ctx context.Context) {
	ms, err := m.Store.ListImageMirrors(ctx)
	if err != nil {
		log.Printf("image mirrors: list: %v", err)
		return
	}
	for _, mr := range ms {
		if err := m.RunOne(ctx, mr.ID); err != nil {
			log.Printf("image mirror %s:%s: %v", mr.Upstream, mr.Tag, err)
		}
	}
}

// Start clears leftover staging, then runs every mirror a minute after boot and every interval
// (default 24 h, like sources) until ctx ends.
func (m *Mirrors) Start(ctx context.Context, interval time.Duration) {
	if m.StagingDir != "" {
		os.RemoveAll(m.StagingDir) // only partial downloads of runs that died with the process
	}
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	go func() {
		t := time.NewTimer(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				m.RunAll(ctx)
				t.Reset(interval)
			}
		}
	}()
}
