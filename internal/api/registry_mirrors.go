package api

import (
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"time"

	"dnfrepo/internal/imagemirror"
	"dnfrepo/internal/store"
)

type imageMirrorJSON struct {
	ID             int64      `json:"id"`
	Upstream       string     `json:"upstream"`
	Tag            string     `json:"tag"`
	LocalName      string     `json:"local_name"`
	Platforms      []string   `json:"platforms"`
	Keep           int        `json:"keep"`
	Username       string     `json:"username"`
	HasCredentials bool       `json:"has_credentials"`
	UpstreamDigest string     `json:"upstream_digest"`
	LastRun        *time.Time `json:"last_run"`
	LastStatus     string     `json:"last_status"`
	LastError      string     `json:"last_error"`
}

func toImageMirrorJSON(m store.ImageMirror) imageMirrorJSON {
	return imageMirrorJSON{m.ID, m.Upstream, m.Tag, m.LocalName, m.Platforms, m.Keep, m.Username, m.Secret != nil,
		m.UpstreamDigest, m.LastRun, m.LastStatus, m.LastError}
}

var platformRE = regexp.MustCompile(`^[a-z0-9]+/[a-z0-9_]+(/[a-z0-9]+)?$`)

func validPlatforms(ps []string) bool {
	for _, p := range ps {
		if !platformRE.MatchString(p) {
			return false
		}
	}
	return true
}

// seal returns the sealed token, or an error message for the client.
func (a *RegistryAPI) seal(username, token string) ([]byte, string) {
	if (username == "") != (token == "") {
		return nil, "username and token go together"
	}
	if token == "" {
		return nil, ""
	}
	if a.Mirrors.Box == nil {
		return nil, "upstream credentials need SECRET_KEY to be set on the server"
	}
	b, err := a.Mirrors.Box.Seal(token)
	if err != nil {
		return nil, "cannot seal credentials"
	}
	return b, ""
}

// runDetached starts a mirror run that outlives the request. A run already in flight picks up
// a platform edit itself when it finishes.
func (a *RegistryAPI) runDetached(r *http.Request, id int64) error {
	return a.Mirrors.Trigger(r.Context(), id)
}

func (a *RegistryAPI) mirrorRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/registry/mirrors", func(w http.ResponseWriter, r *http.Request) {
		ms, err := a.Store.ListImageMirrors(r.Context())
		if err != nil {
			errJSON(w, 500, "internal error")
			return
		}
		out := []imageMirrorJSON{}
		for _, m := range ms {
			out = append(out, toImageMirrorJSON(m))
		}
		writeJSON(w, 200, out)
	})

	mux.HandleFunc("POST /api/registry/mirrors", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Upstream  string   `json:"upstream"`
			LocalName string   `json:"local_name"`
			Platforms []string `json:"platforms"`
			Keep      int      `json:"keep"`
			Username  string   `json:"username"`
			Token     string   `json:"token"`
		}
		if !decode(w, r, &in) {
			return
		}
		ref, err := imagemirror.ParseRef(in.Upstream)
		if err != nil {
			errJSON(w, 400, err.Error())
			return
		}
		if in.LocalName == "" {
			in.LocalName = ref.DefaultLocalName()
		}
		if in.Keep == 0 {
			in.Keep = 3
		}
		switch {
		case !validName(in.LocalName):
			errJSON(w, 400, "invalid local name")
			return
		case in.Keep < 1:
			errJSON(w, 400, "keep must be at least 1")
			return
		case !validPlatforms(in.Platforms):
			errJSON(w, 400, "platforms are os/arch[/variant], e.g. linux/arm64")
			return
		}
		sealed, msg := a.seal(in.Username, in.Token)
		if msg != "" {
			errJSON(w, 400, msg)
			return
		}
		m, err := a.Store.AddImageMirror(r.Context(), store.ImageMirror{Upstream: ref.Name(), Tag: ref.Tag, LocalName: in.LocalName,
			Platforms: in.Platforms, Keep: in.Keep, Username: in.Username, Secret: sealed})
		switch {
		case errors.Is(err, store.ErrMirrorExists):
			errJSON(w, 409, err.Error())
			return
		case err != nil:
			errJSON(w, 500, "internal error")
			return
		}
		a.log(r, "added image mirror "+ref.String()+" -> "+m.LocalName)
		a.runDetached(r, m.ID)
		writeJSON(w, 201, toImageMirrorJSON(m))
	})

	mux.HandleFunc("PUT /api/registry/mirrors/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			errJSON(w, 400, "bad id")
			return
		}
		var in struct {
			Platforms []string `json:"platforms"`
			Keep      int      `json:"keep"`
			Username  *string  `json:"username"` // omitted: keep credentials; "": clear them
			Token     string   `json:"token"`
		}
		if !decode(w, r, &in) {
			return
		}
		if in.Keep < 1 || !validPlatforms(in.Platforms) {
			errJSON(w, 400, "keep must be at least 1 and platforms os/arch[/variant]")
			return
		}
		cur, err := a.Store.ImageMirrorByID(r.Context(), id)
		if errors.Is(err, store.ErrNotFound) {
			errJSON(w, 404, "not found")
			return
		} else if err != nil {
			errJSON(w, 500, "internal error")
			return
		}
		var sealed []byte
		if in.Username != nil {
			var msg string
			if sealed, msg = a.seal(*in.Username, in.Token); msg != "" {
				errJSON(w, 400, msg)
				return
			}
		}
		a2, b2 := slices.Clone(cur.Platforms), slices.Clone(in.Platforms)
		slices.Sort(a2)
		slices.Sort(b2)
		changed := !slices.Equal(a2, b2)
		if err := a.Store.UpdateImageMirror(r.Context(), id, in.Platforms, in.Keep, in.Username, sealed, changed); err != nil {
			errJSON(w, 500, "internal error")
			return
		}
		a.log(r, "updated image mirror "+strconv.FormatInt(id, 10))
		if changed {
			a.runDetached(r, id)
		}
		m, _ := a.Store.ImageMirrorByID(r.Context(), id)
		writeJSON(w, 200, toImageMirrorJSON(m))
	})

	mux.HandleFunc("DELETE /api/registry/mirrors/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			errJSON(w, 400, "bad id")
			return
		}
		switch err := a.Store.DeleteImageMirror(r.Context(), id); {
		case errors.Is(err, store.ErrNotFound):
			errJSON(w, 404, "not found")
		case err != nil:
			errJSON(w, 500, "internal error")
		default:
			a.log(r, "removed image mirror "+strconv.FormatInt(id, 10)+" (local image kept)")
			writeJSON(w, 200, map[string]string{"status": "removed"})
		}
	})

	mux.HandleFunc("POST /api/registry/mirrors/{id}/run", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			errJSON(w, 400, "bad id")
			return
		}
		if _, err := a.Store.ImageMirrorByID(r.Context(), id); err != nil {
			errJSON(w, 404, "not found")
			return
		}
		if err := a.runDetached(r, id); errors.Is(err, imagemirror.ErrRunning) {
			errJSON(w, 409, "already running")
			return
		}
		writeJSON(w, 202, map[string]string{"status": "started"})
	})

	mux.HandleFunc("POST /api/registry/mirrors/inspect", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ID       int64  `json:"id"`
			Upstream string `json:"upstream"`
			Username string `json:"username"`
			Token    string `json:"token"`
		}
		if !decode(w, r, &in) {
			return
		}
		var (
			dg  string
			ps  []string
			err error
		)
		if in.ID != 0 {
			if _, gerr := a.Store.ImageMirrorByID(r.Context(), in.ID); gerr != nil {
				errJSON(w, 404, "not found")
				return
			}
			dg, ps, err = a.Mirrors.InspectMirror(r.Context(), in.ID)
		} else {
			if _, perr := imagemirror.ParseRef(in.Upstream); perr != nil {
				errJSON(w, 400, perr.Error())
				return
			}
			dg, ps, err = a.Mirrors.Inspect(r.Context(), in.Upstream, in.Username, in.Token)
		}
		if err != nil {
			errJSON(w, 502, err.Error()) // upstream errors never carry the credentials
			return
		}
		writeJSON(w, 200, map[string]any{"digest": dg, "platforms": ps})
	})
}
