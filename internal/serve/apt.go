package serve

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"dnfrepo/internal/store"
)

var (
	debRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+~-]*\.deb$`)
	aptSeg = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+~-]*$`)
)

// aptFile serves a deb repo: dists/<suite>/... (through the dists symlink, regular files only) and
// pool/<component>/<file>.deb, which maps to the flat file <repo>/<file>.deb. Nothing else is served.
func (d Deps) aptFile(w http.ResponseWriter, r *http.Request, repo store.Repo, p string) {
	cfg, err := d.Store.DebRepoByID(r.Context(), repo.ID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	dir := d.RepoDir(repo.Slug)
	parts := strings.Split(p, "/")
	var full string
	switch {
	case parts[0] == "dists" && len(parts) >= 3 && parts[1] == cfg.Suite && allSegs(parts[1:]):
		full = filepath.Join(append([]string{dir, "dists"}, parts[1:]...)...)
	case parts[0] == "pool" && len(parts) == 3 && slices.Contains(cfg.Components, parts[1]) && debRe.MatchString(parts[2]):
		full = filepath.Join(dir, parts[2])
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

func allSegs(parts []string) bool {
	for _, s := range parts {
		if !aptSeg.MatchString(s) {
			return false
		}
	}
	return true
}
