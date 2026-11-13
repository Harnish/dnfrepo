package serve

import (
	_ "embed"
	"fmt"
	"html/template"
	"net/http"
	"strings"

	"dnfrepo/internal/store"
)

//go:embed templates/browse.html
var browseSrc string

var browseTmpl = template.Must(template.New("browse").Funcs(template.FuncMap{
	"size": func(n int64) string {
		const k = 1024.0
		f := float64(n)
		switch {
		case f >= k*k*k:
			return fmt.Sprintf("%.1f GiB", f/(k*k*k))
		case f >= k*k:
			return fmt.Sprintf("%.1f MiB", f/(k*k))
		case f >= k:
			return fmt.Sprintf("%.1f KiB", f/k)
		}
		return fmt.Sprintf("%d B", n)
	},
}).Parse(browseSrc))

func (d Deps) browse(w http.ResponseWriter, r *http.Request, repo store.Repo) {
	pkgs, err := d.Store.ListPackages(r.Context(), repo.ID)
	if err != nil {
		http.Error(w, "internal error", 500)
		return
	}
	data := map[string]any{"Repo": repo, "Packages": pkgs, "BaseURL": d.BaseURL}
	if repo.Kind == store.KindDeb {
		cfg, err := d.Store.DebRepoByID(r.Context(), repo.ID)
		if err != nil {
			http.Error(w, "internal error", 500)
			return
		}
		data["Deb"] = cfg
		data["Components"] = strings.Join(cfg.Components, " ")
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	browseTmpl.Execute(w, data)
}
