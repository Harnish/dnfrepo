package registry

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"

	"dnfrepo/internal/store"
)

func (r *Registry) Gate() *Gate { return r.gate }

// FrontHandler is what dnfrepo mounts at /v2/: it serves the filtered catalog itself, rejects
// writes while the gate is closed, and hands everything else to the registry.
func (r *Registry) FrontHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/v2/_catalog" && (req.Method == http.MethodGet || req.Method == http.MethodHead) {
			r.serveCatalog(w, req)
			return
		}
		if req.URL.Path == "/v2/" && req.Header.Get("Authorization") == "" {
			// The standard handshake: an unauthenticated ping gets a Basic challenge, which makes
			// docker, podman and containerd send saved credentials afterwards. Anonymous pulls
			// still work, because every later request is authorized on its own.
			w.Header().Set("WWW-Authenticate", `Basic realm="dnfrepo registry"`)
			w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		if req.Method != http.MethodGet && req.Method != http.MethodHead {
			if !r.gate.Begin() {
				w.Header().Set("Retry-After", "60")
				http.Error(w, "registry is read-only for maintenance", http.StatusServiceUnavailable)
				return
			}
			defer r.gate.End()
		}
		r.app.ServeHTTP(w, req)
	})
}

// visibleRepos lists the repos the caller may see: everything for a push token; otherwise
// repos it can pull that are Listed, plus users-mode repos it proved a grant on. Unlisted
// public repos stay hidden from everyone but push tokens, whatever credentials are sent.
func (r *Registry) visibleRepos(req *http.Request) ([]string, Verdict, error) {
	c, v := r.policy.Identify(req)
	if v != Allow {
		return nil, v, nil
	}
	names, err := r.Repos(req.Context())
	if err != nil {
		return nil, Error, err
	}
	var out []string
	for _, n := range names {
		if c.Push {
			out = append(out, n)
			continue
		}
		row, err := r.policy.Store.RegistryRepoByName(req.Context(), n)
		if err != nil {
			return nil, Error, err
		}
		if !row.Listed && row.Mode != store.ModeUsers {
			continue
		}
		ok, err := r.policy.CanPull(req, c, n)
		if err != nil {
			return nil, Error, err
		}
		if ok {
			out = append(out, n)
		}
	}
	r.policy.release(req, c)
	return out, Allow, nil
}

func (r *Registry) serveCatalog(w http.ResponseWriter, req *http.Request) {
	n := 100
	if v := req.URL.Query().Get("n"); v != "" {
		var err error
		if n, err = strconv.Atoi(v); err != nil || n < 1 || n > 10000 {
			http.Error(w, "bad n", http.StatusBadRequest)
			return
		}
	}
	names, v, err := r.visibleRepos(req)
	switch v {
	case Throttled:
		w.Header().Set("Retry-After", "300")
		http.Error(w, "too many failed attempts", http.StatusTooManyRequests)
		return
	case Error:
		http.Error(w, "internal error", http.StatusInternalServerError)
		_ = err
		return
	}
	if last := req.URL.Query().Get("last"); last != "" {
		i := sort.SearchStrings(names, last)
		if i < len(names) && names[i] == last {
			i++
		}
		names = names[i:]
	}
	more := len(names) > n
	if more {
		names = names[:n]
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if more {
		q := url.Values{"last": {names[len(names)-1]}, "n": {strconv.Itoa(n)}}
		w.Header().Set("Link", fmt.Sprintf(`</v2/_catalog?%s>; rel="next"`, q.Encode()))
	}
	if names == nil {
		names = []string{}
	}
	json.NewEncoder(w).Encode(map[string][]string{"repositories": names})
}
