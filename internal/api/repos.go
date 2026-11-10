// Package api holds the admin JSON API handlers.
package api

import (
	"errors"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"

	"dnfrepo/internal/store"
)

// Repos serves repo CRUD, pipeline links, and IP allowlists.
type Repos struct {
	Store    *store.Store
	DataDir  string
	OnChange func(slug string) // called after a repo is created; wired to Publisher.Rebuild
}

type repoJSON struct {
	Slug     string   `json:"slug"`
	Name     string   `json:"name"`
	Mode     string   `json:"mode"`
	Previous *string  `json:"previous"`
	IPs      []string `json:"ips"`

	Keep          int            `json:"keep"`
	KeepOverrides map[string]int `json:"keep_overrides"`
}

func (a *Repos) toJSON(r *http.Request, rp store.Repo, byID map[int64]string) (repoJSON, error) {
	out := repoJSON{Slug: rp.Slug, Name: rp.Name, Mode: string(rp.Mode), IPs: []string{}}
	if rp.PreviousID != nil {
		p := byID[*rp.PreviousID]
		out.Previous = &p
	}
	keep, ov, kerr := a.Store.RepoKeep(r.Context(), rp.ID)
	if kerr != nil {
		return out, kerr
	}
	out.Keep, out.KeepOverrides = keep, ov
	ips, err := a.Store.RepoIPs(r.Context(), rp.ID)
	for _, p := range ips {
		out.IPs = append(out.IPs, p.String())
	}
	return out, err
}

func (a *Repos) storeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		errJSON(w, 404, err.Error())
	case errors.Is(err, store.ErrBadSlug):
		errJSON(w, 400, err.Error())
	case errors.Is(err, store.ErrChain), errors.Is(err, store.ErrInChain), errors.Is(err, store.ErrExists), errors.Is(err, store.ErrHasContent), errors.Is(err, store.ErrMirrorRepo):
		errJSON(w, 409, err.Error())
	default:
		errJSON(w, 500, "internal error")
	}
}

func (a *Repos) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/repos", func(w http.ResponseWriter, r *http.Request) {
		repos, err := a.Store.ListRepos(r.Context())
		if err != nil {
			a.storeErr(w, err)
			return
		}
		byID := map[int64]string{}
		for _, rp := range repos {
			byID[rp.ID] = rp.Slug
		}
		out := []repoJSON{}
		for _, rp := range repos {
			j, err := a.toJSON(r, rp, byID)
			if err != nil {
				a.storeErr(w, err)
				return
			}
			out = append(out, j)
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("POST /api/repos", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Slug, Name, Mode string
			Previous         *string
		}
		if !decode(w, r, &in) {
			return
		}
		var prev *int64
		if in.Previous != nil && *in.Previous != "" {
			p, err := a.Store.RepoBySlug(r.Context(), *in.Previous)
			if err != nil {
				errJSON(w, 409, "previous repo not found")
				return
			}
			prev = &p.ID
		}
		if in.Name == "" {
			in.Name = in.Slug
		}
		rp, err := a.Store.CreateRepo(r.Context(), in.Slug, in.Name, store.AccessMode(in.Mode), prev)
		if err != nil {
			a.storeErr(w, err)
			return
		}
		if err := os.MkdirAll(filepath.Join(a.DataDir, "repos", rp.Slug), 0o755); err != nil {
			errJSON(w, 500, "cannot create repo dir")
			return
		}
		if a.OnChange != nil {
			a.OnChange(rp.Slug)
		}
		j, _ := a.toJSON(r, rp, map[int64]string{})
		if in.Previous != nil {
			j.Previous = in.Previous
		}
		writeJSON(w, 201, j)
	})
	mux.HandleFunc("GET /api/repos/{slug}", func(w http.ResponseWriter, r *http.Request) {
		rp, err := a.Store.RepoBySlug(r.Context(), r.PathValue("slug"))
		if err != nil {
			a.storeErr(w, err)
			return
		}
		byID := map[int64]string{}
		if rp.PreviousID != nil {
			if p, err := a.Store.RepoByID(r.Context(), *rp.PreviousID); err == nil {
				byID[p.ID] = p.Slug
			}
		}
		j, err := a.toJSON(r, rp, byID)
		if err != nil {
			a.storeErr(w, err)
			return
		}
		writeJSON(w, 200, j)
	})
	mux.HandleFunc("PATCH /api/repos/{slug}", func(w http.ResponseWriter, r *http.Request) {
		rp, err := a.Store.RepoBySlug(r.Context(), r.PathValue("slug"))
		if err != nil {
			a.storeErr(w, err)
			return
		}
		var in struct{ Name, Mode string }
		if !decode(w, r, &in) {
			return
		}
		if in.Name == "" {
			in.Name = rp.Name
		}
		if in.Mode == "" {
			in.Mode = string(rp.Mode)
		}
		if !store.AccessMode(in.Mode).Valid() {
			errJSON(w, 400, "invalid mode")
			return
		}
		if err := a.Store.UpdateRepo(r.Context(), rp.ID, in.Name, store.AccessMode(in.Mode)); err != nil {
			a.storeErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("PUT /api/repos/{slug}/previous", func(w http.ResponseWriter, r *http.Request) {
		rp, err := a.Store.RepoBySlug(r.Context(), r.PathValue("slug"))
		if err != nil {
			a.storeErr(w, err)
			return
		}
		var in struct{ Previous *string }
		if !decode(w, r, &in) {
			return
		}
		var prev *int64
		if in.Previous != nil && *in.Previous != "" {
			p, err := a.Store.RepoBySlug(r.Context(), *in.Previous)
			if err != nil {
				errJSON(w, 409, "previous repo not found")
				return
			}
			prev = &p.ID
		}
		if err := a.Store.SetPrevious(r.Context(), rp.ID, prev); err != nil {
			a.storeErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("DELETE /api/repos/{slug}", func(w http.ResponseWriter, r *http.Request) {
		rp, err := a.Store.RepoBySlug(r.Context(), r.PathValue("slug"))
		if err != nil {
			a.storeErr(w, err)
			return
		}
		if err := a.Store.DeleteRepo(r.Context(), rp.ID); err != nil {
			a.storeErr(w, err)
			return
		}
		os.RemoveAll(filepath.Join(a.DataDir, "repos", rp.Slug))
		writeJSON(w, 200, map[string]string{"status": "deleted"})
	})
	mux.HandleFunc("PUT /api/repos/{slug}/ips", func(w http.ResponseWriter, r *http.Request) {
		rp, err := a.Store.RepoBySlug(r.Context(), r.PathValue("slug"))
		if err != nil {
			a.storeErr(w, err)
			return
		}
		var in struct{ IPs []string }
		if !decode(w, r, &in) {
			return
		}
		var ps []netip.Prefix
		for _, s := range in.IPs {
			s = strings.TrimSpace(s)
			p, err := netip.ParsePrefix(s)
			if err != nil {
				a, err2 := netip.ParseAddr(s)
				if err2 != nil {
					errJSON(w, 400, "invalid IP or CIDR: "+s)
					return
				}
				p = netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen())
			}
			ps = append(ps, p)
		}
		if err := a.Store.SetRepoIPs(r.Context(), rp.ID, ps); err != nil {
			a.storeErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]string{"status": "ok"})
	})
}
