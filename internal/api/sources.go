package api

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"

	"dnfrepo/internal/sources"
	"dnfrepo/internal/store"
)

// Sources manages a first-stage repo's GitHub/Plex sources.
type Sources struct {
	Store     *store.Store
	Scheduler *sources.Scheduler
}

var ownerNameRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

type sourceJSON struct {
	ID         int64   `json:"id"`
	Kind       string  `json:"kind"`
	Spec       string  `json:"spec"`
	LastRun    *string `json:"last_run"`
	LastStatus string  `json:"last_status"`
	LastError  string  `json:"last_error"`
}

func toSourceJSON(s store.Source) sourceJSON {
	j := sourceJSON{ID: s.ID, Kind: s.Kind, Spec: s.Spec, LastStatus: s.LastStatus, LastError: s.LastError}
	if s.LastRun != nil {
		t := s.LastRun.UTC().Format("2006-01-02T15:04:05Z")
		j.LastRun = &t
	}
	return j
}

func (a *Sources) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/repos/{slug}/sources", func(w http.ResponseWriter, r *http.Request) {
		repo, err := a.Store.RepoBySlug(r.Context(), r.PathValue("slug"))
		if err != nil {
			errJSON(w, 404, "not found")
			return
		}
		ss, err := a.Store.ListSources(r.Context(), repo.ID)
		if err != nil {
			errJSON(w, 500, "internal error")
			return
		}
		out := []sourceJSON{}
		for _, s := range ss {
			out = append(out, toSourceJSON(s))
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("POST /api/repos/{slug}/sources", func(w http.ResponseWriter, r *http.Request) {
		repo, err := a.Store.RepoBySlug(r.Context(), r.PathValue("slug"))
		if err != nil {
			errJSON(w, 404, "not found")
			return
		}
		if repo.PreviousID != nil {
			errJSON(w, 409, "sources can only be attached to the first stage of a pipeline")
			return
		}
		var in struct{ Kind, Spec string }
		if !decode(w, r, &in) {
			return
		}
		switch in.Kind {
		case "github":
			in.Spec = sources.RepoName(in.Spec)
			if !ownerNameRe.MatchString(in.Spec) {
				errJSON(w, 400, "github spec must be owner/name or a github.com URL")
				return
			}
		case "plex":
			in.Spec = ""
		default:
			errJSON(w, 400, "kind must be github or plex")
			return
		}
		s, err := a.Store.AddSource(r.Context(), repo.ID, in.Kind, in.Spec)
		if err != nil {
			errJSON(w, 500, "internal error")
			return
		}
		writeJSON(w, 201, toSourceJSON(s))
	})
	mux.HandleFunc("DELETE /api/sources/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			errJSON(w, 400, "bad id")
			return
		}
		if err := a.Store.DeleteSource(r.Context(), id); errors.Is(err, store.ErrNotFound) {
			errJSON(w, 404, "not found")
		} else if err != nil {
			errJSON(w, 500, "internal error")
		} else {
			writeJSON(w, 200, map[string]string{"status": "deleted"})
		}
	})
	mux.HandleFunc("POST /api/sources/{id}/run", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			errJSON(w, 400, "bad id")
			return
		}
		if _, err := a.Store.SourceByID(r.Context(), id); err != nil {
			errJSON(w, 404, "not found")
			return
		}
		go a.Scheduler.RunOne(context.Background(), id) // downloads can outlast the request
		writeJSON(w, 202, map[string]string{"status": "started"})
	})
}
