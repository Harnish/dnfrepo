package api

import (
	"errors"
	"net/http"

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
	case errors.Is(err, pipeline.ErrNotFirstStage), errors.Is(err, pipeline.ErrExists):
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
}

func toPkgJSON(p store.Package) pkgJSON {
	return pkgJSON{p.Filename, p.Name, p.Version, p.Release, p.Arch, p.SHA256, p.Size, p.AddedAt.UTC().Format("2006-01-02T15:04:05Z")}
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
