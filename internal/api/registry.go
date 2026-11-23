package api

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/distribution/reference"

	"dnfrepo/internal/imagemirror"
	"dnfrepo/internal/registry"
	"dnfrepo/internal/store"
)

// RegistryAPI manages the embedded container registry (admin) and serves the read-only
// public browse data. Image names contain '/', so they travel in the `name` query parameter.
type RegistryAPI struct {
	Store      *store.Store
	Reg        *registry.Registry
	Policy     *registry.Policy
	Actor      func(*http.Request) string
	PublicHost string               // host shown in pull commands
	Mirrors    *imagemirror.Mirrors // nil: no image mirror routes
}

type regTag struct {
	Name    string     `json:"name"`
	Digest  string     `json:"digest"`
	Size    int64      `json:"size"`
	Created *time.Time `json:"created"`
	Arches  []string   `json:"arches"`
	Layers  []regLayer `json:"layers"`
	Pull    string     `json:"pull"`
}

type regLayer struct {
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

func (a *RegistryAPI) tags(ctx context.Context, name string) ([]regTag, error) {
	ts, err := a.Reg.Tags(ctx, name)
	if err != nil {
		return nil, err
	}
	out := make([]regTag, 0, len(ts))
	for _, t := range ts {
		rt := regTag{Name: t.Name, Digest: t.Digest, Size: t.Size, Created: t.Created, Arches: t.Arches,
			Layers: []regLayer{}, Pull: a.PublicHost + "/" + name + ":" + t.Name}
		if rt.Arches == nil {
			rt.Arches = []string{}
		}
		for _, l := range t.Layers {
			rt.Layers = append(rt.Layers, regLayer{l.Digest, l.Size})
		}
		out = append(out, rt)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func validName(name string) bool {
	_, err := reference.WithName(name)
	return name != "" && err == nil
}

func (a *RegistryAPI) log(r *http.Request, msg string) {
	log.Printf("registry: %s: %s", a.Actor(r), msg) // never includes tokens
}

func (a *RegistryAPI) Routes(mux *http.ServeMux) {
	if a.Mirrors != nil {
		a.mirrorRoutes(mux)
	}
	mux.HandleFunc("GET /api/registry/repos", func(w http.ResponseWriter, r *http.Request) {
		names, err := a.Reg.Repos(r.Context())
		if err != nil {
			errJSON(w, 500, "internal error")
			return
		}
		rows, err := a.Store.ListRegistryRepos(r.Context())
		if err != nil {
			errJSON(w, 500, "internal error")
			return
		}
		byName := map[string]store.RegistryRepo{}
		for _, rw := range rows {
			byName[rw.Name] = rw
		}
		inStore := map[string]bool{}
		out := []map[string]any{}
		add := func(n string, empty bool) {
			row, ok := byName[n]
			if !ok {
				row = store.RegistryRepo{Name: n, Mode: store.ModePublic}
			}
			item := map[string]any{"name": n, "listed": row.Listed, "mode": row.Mode, "tags": 0, "empty": empty}
			if !empty {
				if ts, err := a.Reg.TagNames(r.Context(), n); err == nil {
					item["tags"] = len(ts)
				}
			}
			out = append(out, item)
		}
		for _, n := range names {
			inStore[n] = true
			add(n, false)
		}
		for _, rw := range rows {
			if !inStore[rw.Name] {
				add(rw.Name, true)
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i]["name"].(string) < out[j]["name"].(string) })
		writeJSON(w, 200, out)
	})

	mux.HandleFunc("GET /api/registry/repo", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		if !validName(name) {
			errJSON(w, 400, "bad image name")
			return
		}
		row, err := a.Store.RegistryRepoByName(r.Context(), name)
		if err != nil {
			errJSON(w, 500, "internal error")
			return
		}
		ts, err := a.tags(r.Context(), name)
		if err != nil && !errors.Is(err, registry.ErrNoSuchRepo) {
			errJSON(w, 500, "internal error")
			return
		}
		if errors.Is(err, registry.ErrNoSuchRepo) && !row.Exists {
			errJSON(w, 404, "not found")
			return
		}
		if ts == nil {
			ts = []regTag{}
		}
		ips, _ := a.Store.RegistryIPs(r.Context(), name)
		strs := []string{}
		for _, p := range ips {
			strs = append(strs, p.String())
		}
		ids, _ := a.Store.RegistryGrants(r.Context(), name)
		if ids == nil {
			ids = []int64{}
		}
		writeJSON(w, 200, map[string]any{"name": name, "listed": row.Listed, "mode": row.Mode,
			"ips": strs, "user_ids": ids, "tags": ts})
	})

	mux.HandleFunc("PUT /api/registry/repo", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		var in struct {
			Listed  bool             `json:"listed"`
			Mode    store.AccessMode `json:"mode"`
			IPs     []string         `json:"ips"`
			UserIDs []int64          `json:"user_ids"`
		}
		if !validName(name) {
			errJSON(w, 400, "bad image name")
			return
		}
		if !decode(w, r, &in) {
			return
		}
		if !in.Mode.Valid() {
			errJSON(w, 400, store.ErrBadMode.Error())
			return
		}
		var ps []netip.Prefix
		for _, s := range in.IPs {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			p, err := netip.ParsePrefix(s)
			if err != nil {
				ad, err2 := netip.ParseAddr(s)
				if err2 != nil {
					errJSON(w, 400, "bad IP or CIDR: "+s)
					return
				}
				p = netip.PrefixFrom(ad, ad.BitLen())
			}
			ps = append(ps, p)
		}
		ctx := r.Context()
		if err := a.Store.SetRegistryRepo(ctx, name, in.Listed, in.Mode); err != nil {
			errJSON(w, 500, "internal error")
			return
		}
		if err := a.Store.SetRegistryIPs(ctx, name, ps); err != nil {
			errJSON(w, 500, "internal error")
			return
		}
		if err := a.Store.SetRegistryGrants(ctx, name, in.UserIDs); err != nil {
			errJSON(w, 500, "internal error")
			return
		}
		a.log(r, "settings "+name+" listed="+strconv.FormatBool(in.Listed)+" mode="+string(in.Mode))
		writeJSON(w, 200, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("DELETE /api/registry/repo", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		if !validName(name) {
			errJSON(w, 400, "bad image name")
			return
		}
		if err := a.Store.DeleteRegistryRepo(r.Context(), name); err != nil {
			errJSON(w, 500, "internal error")
			return
		}
		a.log(r, "deleted settings "+name)
		writeJSON(w, 200, map[string]string{"status": "deleted"})
	})

	mux.HandleFunc("DELETE /api/registry/tag", func(w http.ResponseWriter, r *http.Request) {
		name, tag := r.URL.Query().Get("name"), r.URL.Query().Get("tag")
		if !validName(name) || !registry.ValidTag(tag) {
			errJSON(w, 400, "bad image name or tag")
			return
		}
		switch err := a.Reg.DeleteTag(r.Context(), name, tag); {
		case errors.Is(err, registry.ErrNoSuchRepo), errors.Is(err, registry.ErrNoSuchTag):
			errJSON(w, 404, "not found")
		case errors.Is(err, registry.ErrReadOnly):
			errJSON(w, 503, "registry is read-only for maintenance (garbage collection running)")
		case err != nil:
			errJSON(w, 500, "internal error")
		default:
			a.log(r, "deleted tag "+name+":"+tag)
			writeJSON(w, 200, map[string]string{"status": "deleted"})
		}
	})

	mux.HandleFunc("POST /api/registry/gc", func(w http.ResponseWriter, r *http.Request) {
		if a.Reg.GCStatus().State == registry.GCRunning {
			errJSON(w, 409, "garbage collection already running")
			return
		}
		a.log(r, "gc started")
		go func() {
			if _, err := a.Reg.GC(context.Background()); err != nil {
				log.Printf("registry: gc: %v", err)
			}
		}()
		writeJSON(w, 202, map[string]string{"status": "started"})
	})
	mux.HandleFunc("GET /api/registry/gc", func(w http.ResponseWriter, r *http.Request) {
		s := a.Reg.GCStatus()
		writeJSON(w, 200, map[string]any{"state": s.State, "removed": s.Result.Removed, "bytes_freed": s.Result.BytesFreed,
			"duration_ms": s.Result.Duration.Milliseconds(), "error": s.Err, "started": s.Started})
	})

	mux.HandleFunc("GET /api/registry/push-tokens", func(w http.ResponseWriter, r *http.Request) {
		ts, err := a.Store.ListPushTokens(r.Context())
		if err != nil {
			errJSON(w, 500, "internal error")
			return
		}
		out := []map[string]any{}
		for _, t := range ts {
			out = append(out, map[string]any{"id": t.ID, "name": t.Name, "revoked": t.Revoked, "created_by": t.CreatedBy, "created_at": t.CreatedAt})
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("POST /api/registry/push-tokens", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Name string }
		if !decode(w, r, &in) {
			return
		}
		id, tok, err := a.Store.CreatePushToken(r.Context(), in.Name, a.Actor(r))
		switch {
		case errors.Is(err, store.ErrExists):
			errJSON(w, 409, "token name exists")
		case err != nil:
			errJSON(w, 400, err.Error())
		default:
			a.log(r, "created push token "+in.Name)
			writeJSON(w, 201, map[string]any{"id": id, "name": in.Name, "token": tok}) // only time the token is shown
		}
	})
	mux.HandleFunc("DELETE /api/registry/push-tokens/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			errJSON(w, 400, "bad id")
			return
		}
		switch err := a.Store.RevokePushToken(r.Context(), id); {
		case errors.Is(err, store.ErrNotFound):
			errJSON(w, 404, "not found")
		case err != nil:
			errJSON(w, 500, "internal error")
		default:
			a.log(r, "revoked push token "+strconv.FormatInt(id, 10))
			writeJSON(w, 200, map[string]string{"status": "revoked"})
		}
	})
}

// listedPublic reports whether name may appear on the public page: Listed, and readable by an
// anonymous caller from this client address.
func (a *RegistryAPI) listedPublic(r *http.Request, name string) bool {
	row, err := a.Store.RegistryRepoByName(r.Context(), name)
	if err != nil || !row.Listed {
		return false
	}
	ok, err := a.Policy.CanPull(r, registry.Caller{Anonymous: true}, name)
	return err == nil && ok
}

// PublicRoutes are unauthenticated and read-only; they ignore any credentials sent.
func (a *RegistryAPI) PublicRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/public/registry/repos", func(w http.ResponseWriter, r *http.Request) {
		names, err := a.Reg.Repos(r.Context())
		if err != nil {
			errJSON(w, 500, "internal error")
			return
		}
		out := []map[string]any{}
		for _, n := range names {
			if !a.listedPublic(r, n) {
				continue
			}
			item := map[string]any{"name": n, "tags": 0}
			if ts, err := a.Reg.TagNames(r.Context(), n); err == nil {
				item["tags"] = len(ts)
			}
			out = append(out, item)
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("GET /api/public/registry/repo", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		if !validName(name) || !a.listedPublic(r, name) {
			errJSON(w, 404, "not found")
			return
		}
		ts, err := a.tags(r.Context(), name)
		if errors.Is(err, registry.ErrNoSuchRepo) {
			errJSON(w, 404, "not found")
			return
		}
		if err != nil {
			errJSON(w, 500, "internal error")
			return
		}
		writeJSON(w, 200, map[string]any{"name": name, "tags": ts})
	})
}
