package api

import (
	"errors"
	"net/http"
	"strconv"

	"dnfrepo/internal/store"
)

// RepoUsers manages Basic-auth users and their per-repo grants.
type RepoUsers struct{ Store *store.Store }

func (a *RepoUsers) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/repo-users", func(w http.ResponseWriter, r *http.Request) {
		us, err := a.Store.ListRepoUsers(r.Context())
		if err != nil {
			errJSON(w, 500, "internal error")
			return
		}
		out := []map[string]any{}
		for _, u := range us {
			out = append(out, map[string]any{"id": u.ID, "name": u.Name, "revoked": u.Revoked, "created_at": u.CreatedAt})
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("POST /api/repo-users", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Name string }
		if !decode(w, r, &in) {
			return
		}
		id, tok, err := a.Store.CreateRepoUser(r.Context(), in.Name)
		switch {
		case errors.Is(err, store.ErrExists):
			errJSON(w, 409, "user exists")
		case err != nil:
			errJSON(w, 400, err.Error())
		default:
			writeJSON(w, 201, map[string]any{"id": id, "name": in.Name, "token": tok}) // only time the token is shown
		}
	})
	mux.HandleFunc("DELETE /api/repo-users/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			errJSON(w, 400, "bad id")
			return
		}
		if err := a.Store.RevokeRepoUser(r.Context(), id); errors.Is(err, store.ErrNotFound) {
			errJSON(w, 404, "not found")
		} else if err != nil {
			errJSON(w, 500, "internal error")
		} else {
			writeJSON(w, 200, map[string]string{"status": "revoked"})
		}
	})
	mux.HandleFunc("GET /api/repos/{slug}/users", func(w http.ResponseWriter, r *http.Request) {
		rp, err := a.Store.RepoBySlug(r.Context(), r.PathValue("slug"))
		if err != nil {
			errJSON(w, 404, "not found")
			return
		}
		ids, err := a.Store.RepoGrants(r.Context(), rp.ID)
		if err != nil {
			errJSON(w, 500, "internal error")
			return
		}
		if ids == nil {
			ids = []int64{}
		}
		writeJSON(w, 200, map[string]any{"user_ids": ids})
	})
	mux.HandleFunc("PUT /api/repos/{slug}/users", func(w http.ResponseWriter, r *http.Request) {
		rp, err := a.Store.RepoBySlug(r.Context(), r.PathValue("slug"))
		if err != nil {
			errJSON(w, 404, "not found")
			return
		}
		var in struct {
			UserIDs []int64 `json:"user_ids"`
		}
		if !decode(w, r, &in) {
			return
		}
		if err := a.Store.SetRepoGrants(r.Context(), rp.ID, in.UserIDs); err != nil {
			errJSON(w, 400, "invalid user ids")
			return
		}
		writeJSON(w, 200, map[string]string{"status": "ok"})
	})
}
