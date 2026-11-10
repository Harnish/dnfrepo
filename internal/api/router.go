package api

import (
	"io/fs"
	"net/http"
	"path"
	"strings"

	"dnfrepo/internal/adminauth"
)

// Deps are the pieces Router mounts.
type Deps struct {
	Admin     *adminauth.Service
	Repos     *Repos
	RepoUsers *RepoUsers
	Packages  *Packages
	Sources   *Sources
	Mirrors   *Mirrors
	Serve     http.Handler // serve.Handler: /r/... and /gpg.key
	Static    fs.FS        // built React app (index.html at root)
}

// Router mounts: /healthz; public OIDC login endpoints; admin API behind session + CSRF
// header; repo serving (governed by repo access modes, not admin auth); the SPA.
func Router(d Deps) http.Handler {
	top := http.NewServeMux()
	top.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })

	public := http.NewServeMux()
	d.Admin.PublicRoutes(public)
	top.Handle("GET /api/auth/oidc/", public) // browser redirects: GET only, so no CSRF header
	top.Handle("GET /api/auth/methods", public)
	top.Handle("POST /api/auth/local", public) // throttled; 404 unless ADMIN_EMAIL/ADMIN_PASSWORD are set

	admin := http.NewServeMux()
	d.Admin.AdminRoutes(admin)
	d.Repos.Routes(admin)
	d.RepoUsers.Routes(admin)
	d.Packages.Routes(admin)
	d.Packages.RoutesPromote(admin)
	d.Packages.RoutesAudit(admin)
	d.Packages.RoutesKeep(admin)
	d.Sources.Routes(admin)
	d.Mirrors.Routes(admin)
	top.Handle("/api/", d.Admin.RequireAdmin(adminauth.CSRF(admin)))

	top.Handle("GET /r/", d.Serve)
	top.Handle("GET /gpg.key", d.Serve)
	top.Handle("/", spa(d.Static))
	return top
}

// spa serves static files and falls back to index.html for client-side routes.
func spa(fsys fs.FS) http.Handler {
	files := http.FileServerFS(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p == "" {
			p = "index.html"
		}
		if st, err := fs.Stat(fsys, p); err == nil && !st.IsDir() {
			files.ServeHTTP(w, r)
			return
		}
		b, err := fs.ReadFile(fsys, "index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(b)
	})
}
