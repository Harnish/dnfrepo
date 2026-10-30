package serve

import (
	"net/http"

	"dnfrepo/internal/store"
)

func (d Deps) feed(w http.ResponseWriter, r *http.Request, repo store.Repo) { http.NotFound(w, r) }
