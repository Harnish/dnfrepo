package serve

import (
	"fmt"
	"net/http"

	"dnfrepo/internal/store"
)

// repoFile writes the dnf .repo definition. It never contains a real token:
// tokens are stored hashed, so users-mode repos get placeholders.
func (d Deps) repoFile(w http.ResponseWriter, r *http.Request, repo store.Repo) {
	s := fmt.Sprintf("[%s]\nname=%s\nbaseurl=%s/r/%s/\nenabled=1\ngpgcheck=0\nrepo_gpgcheck=1\ngpgkey=%s/gpg.key\n",
		repo.Slug, repo.Name, d.BaseURL, repo.Slug, d.BaseURL)
	if repo.Mode == store.ModeUsers {
		s += "username=YOUR_USERNAME\npassword=YOUR_TOKEN\n"
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.repo"`, repo.Slug))
	w.Write([]byte(s))
}
