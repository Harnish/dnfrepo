package serve

import (
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"dnfrepo/internal/store"
)

// cached serves a package of a cache repo. Only paths listed in the repo's upstream index are
// served: from the local copy when there is one (recording the pull), else from upstream via
// Fetch, which also caches it. A bare /<file> (the browse page's links) serves only a local copy.
func (d Deps) cached(w http.ResponseWriter, r *http.Request, repo store.Repo, p string) {
	if !allSegs(strings.Split(p, "/")) {
		http.NotFound(w, r)
		return
	}
	e, err := d.Store.CacheEntryByPath(r.Context(), repo.ID, p)
	switch {
	case errors.Is(err, store.ErrNotFound) && !strings.Contains(p, "/"):
		e = store.CacheEntry{Filename: p} // local only: no upstream entry to fetch
	case err != nil:
		http.NotFound(w, r)
		return
	}
	if !(rpmRe.MatchString(e.Filename) || debRe.MatchString(e.Filename)) {
		http.NotFound(w, r)
		return
	}
	full := filepath.Join(d.RepoDir(repo.Slug), e.Filename)
	if st, err := os.Lstat(full); err == nil && st.Mode().IsRegular() {
		if f, err := os.Open(full); err == nil {
			defer f.Close()
			http.ServeContent(w, r, e.Filename, st.ModTime(), f)
			if err := d.Store.TouchPackage(r.Context(), repo.ID, e.Filename); err != nil {
				log.Printf("serve: %s: record pull of %s: %v", repo.Slug, e.Filename, err)
			}
			return
		}
	}
	if d.Fetch == nil || e.Href == "" {
		http.NotFound(w, r)
		return
	}
	stall := d.StallTimeout
	if stall <= 0 {
		stall = 60 * time.Second
	}
	lw := &lazyWriter{w: w, rc: http.NewResponseController(w), stall: stall, size: e.Size}
	if err := d.Fetch(r.Context(), repo, e, lw); err != nil {
		log.Printf("serve: %s: fetch %s: %v", repo.Slug, e.Filename, err)
		if !lw.started {
			http.Error(w, "upstream unavailable", http.StatusBadGateway)
		}
	}
}

// lazyWriter sends the 200 header on the first body byte, so a fetch that fails before any
// data can still answer 502. Each write gets its own deadline, so a client that stops reading
// fails the write (the fill goes on without it) instead of blocking it.
type lazyWriter struct {
	w       http.ResponseWriter
	rc      *http.ResponseController
	stall   time.Duration
	size    int64
	started bool
}

func (l *lazyWriter) Write(b []byte) (int, error) {
	l.rc.SetWriteDeadline(time.Now().Add(l.stall)) // unsupported writers (tests) keep no deadline
	if !l.started {
		l.started = true
		h := l.w.Header()
		h.Set("Content-Type", "application/octet-stream")
		h.Set("Content-Length", strconv.FormatInt(l.size, 10))
		l.w.WriteHeader(http.StatusOK)
	}
	return l.w.Write(b)
}
