package api

import (
	"errors"
	"net/http"
	"regexp"

	"dnfrepo/internal/pipeline"
	"dnfrepo/internal/rpmmeta"
	"dnfrepo/internal/store"
)

// Packages serves upload, list, and delete for a repo's rpm files.
type Packages struct {
	Pipeline *pipeline.Service
	Actor    func(*http.Request) string // signed-in admin email
}

func pkgErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		errJSON(w, 404, "not found")
	case errors.Is(err, pipeline.ErrNotFirstStage), errors.Is(err, pipeline.ErrExists), errors.Is(err, store.ErrMirrorRepo):
		errJSON(w, 409, err.Error())
	case errors.Is(err, pipeline.ErrBadFilename), errors.Is(err, rpmmeta.ErrNotRPM):
		errJSON(w, 400, err.Error())
	case errors.Is(err, pipeline.ErrTooLarge):
		errJSON(w, 413, err.Error())
	default:
		errJSON(w, 500, err.Error())
	}
}

type pkgJSON struct {
	Filename string `json:"filename"`
	Name     string `json:"name"`
	Version  string `json:"version"`
	Release  string `json:"release"`
	Arch     string `json:"arch"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
	AddedAt  string `json:"added_at"`
	Batch    *int64 `json:"origin_batch_id"`
}

func toPkgJSON(p store.Package) pkgJSON {
	return pkgJSON{p.Filename, p.Name, p.Version, p.Release, p.Arch, p.SHA256, p.Size, p.AddedAt.UTC().Format("2006-01-02T15:04:05Z"), p.OriginBatchID}
}

func (a *Packages) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/repos/{slug}/packages", func(w http.ResponseWriter, r *http.Request) {
		repo, err := a.Pipeline.Store.RepoBySlug(r.Context(), r.PathValue("slug"))
		if err != nil {
			pkgErr(w, err)
			return
		}
		ps, err := a.Pipeline.Store.ListPackages(r.Context(), repo.ID)
		if err != nil {
			pkgErr(w, err)
			return
		}
		out := []pkgJSON{}
		for _, p := range ps {
			out = append(out, toPkgJSON(p))
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("POST /api/repos/{slug}/packages", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, a.Pipeline.MaxBytes+(1<<20))
		mr, err := r.MultipartReader()
		if err != nil {
			errJSON(w, 400, "multipart form required")
			return
		}
		for {
			part, err := mr.NextPart()
			if err != nil {
				errJSON(w, 400, "missing file part")
				return
			}
			if part.FormName() != "file" {
				continue
			}
			p, err := a.Pipeline.Upload(r.Context(), r.PathValue("slug"), part.FileName(), part, a.Actor(r))
			if err != nil {
				var mbe *http.MaxBytesError
				if errors.As(err, &mbe) {
					err = pipeline.ErrTooLarge
				}
				pkgErr(w, err)
				return
			}
			writeJSON(w, 201, toPkgJSON(p))
			return
		}
	})
	mux.HandleFunc("DELETE /api/repos/{slug}/packages/{filename}", func(w http.ResponseWriter, r *http.Request) {
		if err := a.Pipeline.Delete(r.Context(), r.PathValue("slug"), r.PathValue("filename"), a.Actor(r)); err != nil {
			pkgErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]string{"status": "deleted"})
	})
}

// RoutesPromote registers POST /api/repos/{slug}/promote.
func (a *Packages) RoutesPromote(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/repos/{slug}/promote", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Filenames []string
			Batch     *int64 // alternative to filenames: every package in this repo from that sync batch
		}
		if !decode(w, r, &in) {
			return
		}
		var res pipeline.PromoteResult
		var err error
		switch {
		case in.Batch != nil && in.Filenames != nil:
			errJSON(w, 400, "give filenames or batch, not both")
			return
		case in.Batch != nil:
			res, err = a.Pipeline.PromoteBatch(r.Context(), r.PathValue("slug"), *in.Batch, a.Actor(r))
			if errors.Is(err, pipeline.ErrEmptyPromotion) {
				errJSON(w, 404, "batch has no packages in this repo")
				return
			}
		default:
			res, err = a.Pipeline.Promote(r.Context(), r.PathValue("slug"), in.Filenames, a.Actor(r))
		}
		switch {
		case errors.Is(err, pipeline.ErrNoNextStage), errors.Is(err, pipeline.ErrConflict):
			errJSON(w, 409, err.Error())
		case errors.Is(err, pipeline.ErrEmptyPromotion):
			errJSON(w, 400, err.Error())
		case err != nil:
			pkgErr(w, err)
		default:
			if res.Promoted == nil {
				res.Promoted = []string{}
			}
			if res.Skipped == nil {
				res.Skipped = []string{}
			}
			writeJSON(w, 200, map[string]any{"to": res.To, "promoted": res.Promoted, "skipped": res.Skipped})
		}
	})
}

// RoutesAudit registers GET /api/repos/{slug}/audit (newest 100 events).
func (a *Packages) RoutesAudit(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/repos/{slug}/audit", func(w http.ResponseWriter, r *http.Request) {
		repo, err := a.Pipeline.Store.RepoBySlug(r.Context(), r.PathValue("slug"))
		if err != nil {
			pkgErr(w, err)
			return
		}
		evs, err := a.Pipeline.Store.ListAudit(r.Context(), repo.ID, 100)
		if err != nil {
			pkgErr(w, err)
			return
		}
		out := []map[string]any{}
		for _, e := range evs {
			out = append(out, map[string]any{"id": e.ID, "actor": e.Actor, "action": e.Action, "detail": e.Detail, "at": e.At.UTC().Format("2006-01-02T15:04:05Z")})
		}
		writeJSON(w, 200, out)
	})
}

// RoutesDedup registers POST /api/dedup: hardlink identical rpm files across all repos.
func (a *Packages) RoutesDedup(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/dedup", func(w http.ResponseWriter, r *http.Request) {
		res, err := a.Pipeline.DedupSweep(r.Context(), a.Actor(r))
		if err != nil {
			pkgErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"linked": res.Linked, "bytes_saved": res.BytesSaved, "skipped": res.Skipped})
	})
}

var pkgNameRe =regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)

// RoutesKeep registers PUT /api/repos/{slug}/keep: set the repo's retention limits and apply them now.
func (a *Packages) RoutesKeep(mux *http.ServeMux) {
	mux.HandleFunc("PUT /api/repos/{slug}/keep", func(w http.ResponseWriter, r *http.Request) {
		repo, err := a.Pipeline.Store.RepoBySlug(r.Context(), r.PathValue("slug"))
		if err != nil {
			pkgErr(w, err)
			return
		}
		var in struct {
			Keep      int
			Overrides map[string]int
		}
		if !decode(w, r, &in) {
			return
		}
		for name := range in.Overrides {
			if !pkgNameRe.MatchString(name) {
				errJSON(w, 400, "invalid package name: "+name)
				return
			}
		}
		if err := a.Pipeline.Store.SetRepoKeep(r.Context(), repo.ID, in.Keep, in.Overrides); err != nil {
			if errors.Is(err, store.ErrBadKeep) {
				errJSON(w, 400, err.Error())
				return
			}
			pkgErr(w, err)
			return
		}
		removed, err := a.Pipeline.ApplyKeep(r.Context(), repo.Slug, a.Actor(r))
		if err != nil {
			pkgErr(w, err)
			return
		}
		if removed == nil {
			removed = []string{}
		}
		writeJSON(w, 200, map[string]any{"purged": removed})
	})
}
