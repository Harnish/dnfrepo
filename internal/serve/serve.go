// Package serve exposes each repo over HTTP: files, browse page, .repo file, feed, public key.
package serve

import (
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"dnfrepo/internal/access"
	"dnfrepo/internal/store"
)

type Deps struct {
	Store     *store.Store
	RepoDir   func(slug string) string
	PublicKey string
	BaseURL   string // no trailing slash
	Trusted   []netip.Prefix
}

var (
	rpmRe      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+~-]*\.rpm$`)
	repodataRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+~-]*$`)
)

// Handler serves GET /r/{slug}/..., behind the repo's access guard, and GET /gpg.key.
func Handler(d Deps) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /gpg.key", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pgp-keys")
		w.Write([]byte(d.PublicKey))
	})
	guard := access.Guard(d.Store, d.Trusted)
	mux.Handle("GET /r/{slug}/{path...}", guard(func(r *http.Request) string { return r.PathValue("slug") },
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { d.dispatch(w, r) })))
	return mux
}

func (d Deps) dispatch(w http.ResponseWriter, r *http.Request) {
	repo, err := d.Store.RepoBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	switch p := r.PathValue("path"); {
	case p == "":
		d.browse(w, r, repo)
	case p == "feed.xml":
		d.feed(w, r, repo)
	case p == repo.Slug+".repo":
		d.repoFile(w, r, repo)
	default:
		d.file(w, r, repo, p)
	}
}

// file serves only <name>.rpm and repodata/<name>, as regular files inside the repo dir.
func (d Deps) file(w http.ResponseWriter, r *http.Request, repo store.Repo, p string) {
	dir := d.RepoDir(repo.Slug)
	parts := strings.Split(p, "/")
	var full string
	switch {
	case len(parts) == 1 && rpmRe.MatchString(parts[0]):
		full = filepath.Join(dir, parts[0])
	case len(parts) == 2 && parts[0] == "repodata" && repodataRe.MatchString(parts[1]):
		full = filepath.Join(dir, "repodata", parts[1])
	default:
		http.NotFound(w, r)
		return
	}
	if st, err := os.Lstat(full); err != nil || !st.Mode().IsRegular() { // symlinks and dirs are not served
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(full)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	http.ServeContent(w, r, filepath.Base(full), st.ModTime(), f)
}
