package api

import (
	"errors"
	"log"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"

	"dnfrepo/internal/mirror"
	"dnfrepo/internal/secretbox"
	"dnfrepo/internal/store"
)

// Mirrors serves mirror configuration, manual sync, and sync batches.
// Upstream credentials are write-only: the token is sealed with Box and never returned.
type Mirrors struct {
	Store  *store.Store
	Syncer *mirror.Syncer
	Box    *secretbox.Box // nil when SECRET_KEY is unset: credentials are then refused
	Client *http.Client   // resolves metalink/mirrorlist/gpgkey on PUT; default 30s timeout
	Actor  func(*http.Request) string
}

var resolveClient = &http.Client{Timeout: 30 * time.Second}

func (a *Mirrors) client() *http.Client {
	if a.Client != nil {
		return a.Client
	}
	return resolveClient
}

type scheduleJSON struct {
	Frequency  string `json:"frequency"`
	TimeOfDay  string `json:"time_of_day"`
	Timezone   string `json:"timezone"`
	Weekdays   []int  `json:"weekdays"`
	DayOfMonth int    `json:"day_of_month"`
	Paused     bool   `json:"paused"`
}

type mirrorJSON struct {
	UpstreamURL      string       `json:"upstream_url"`
	Username         string       `json:"username"`
	HasCredentials   bool         `json:"has_credentials"`
	GPGKey           string       `json:"gpg_key"`
	RequireSignature bool         `json:"require_signature"`
	Arches           []string     `json:"arches"`
	Include          []string     `json:"include"`
	Exclude          []string     `json:"exclude"`
	KeepLatest       int          `json:"keep_latest"`
	Schedule         scheduleJSON `json:"schedule"`
	State            string       `json:"state"`
	LastRun          *string      `json:"last_run"`
	LastStatus       string       `json:"last_status"`
	LastError        string       `json:"last_error"`
	NextRun          *string      `json:"next_run"`
}

func ts(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

func toMirrorJSON(m store.Mirror) mirrorJSON {
	sc := m.Schedule
	return mirrorJSON{
		UpstreamURL: m.UpstreamURL, Username: m.Username, HasCredentials: m.Username != "" || len(m.TokenEnc) > 0,
		GPGKey: m.GPGKey, RequireSignature: m.RequireSig,
		Arches: nn(m.Arches), Include: nn(m.Include), Exclude: nn(m.Exclude), KeepLatest: m.KeepLatest,
		Schedule: scheduleJSON{sc.Frequency, sc.TimeOfDay, sc.Timezone, nn(sc.Weekdays), sc.DayOfMonth, sc.Paused},
		State:    m.State, LastRun: ts(m.LastRun), LastStatus: m.LastStatus, LastError: m.LastError, NextRun: ts(m.NextRun),
	}
}

func nn[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}

// sectionURL is the section's upstream URL and its kind: baseurl wins over metalink over mirrorlist.
func sectionURL(s mirror.RepoSection) (string, string) {
	switch {
	case s.BaseURL != "":
		return s.BaseURL, "baseurl"
	case s.Metalink != "":
		return s.Metalink, "metalink"
	}
	return s.MirrorList, "mirrorlist"
}

// keyURL is the first http(s) entry of a section's gpgkey list ("" if none; file:// keys are local to the client).
func keyURL(s mirror.RepoSection) string {
	for _, f := range strings.Fields(strings.ReplaceAll(s.GPGKey, ",", " ")) {
		if strings.HasPrefix(f, "http://") || strings.HasPrefix(f, "https://") {
			return f
		}
	}
	return ""
}

// httpURL accepts only http(s) URLs with a host and no embedded credentials.
func httpURL(u string) bool {
	p, err := url.Parse(u)
	return err == nil && (p.Scheme == "http" || p.Scheme == "https") && p.Host != "" && p.User == nil
}

func (a *Mirrors) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/mirror/parse-repo", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			RepoFile string `json:"repo_file"`
		}
		if !decode(w, r, &in) {
			return
		}
		secs, err := mirror.ParseRepoFile(in.RepoFile)
		if err != nil {
			errJSON(w, 400, err.Error())
			return
		}
		out := []map[string]any{}
		for _, s := range secs {
			u, kind := sectionURL(s)
			out = append(out, map[string]any{"id": s.ID, "name": s.Name, "url_kind": kind,
				"variables": mirror.Variables(u + " " + keyURL(s)), "has_credentials": s.Username != "" || s.Password != ""})
		}
		writeJSON(w, 200, map[string]any{"sections": out})
	})
	mux.HandleFunc("GET /api/repos/{slug}/mirror", func(w http.ResponseWriter, r *http.Request) {
		repo, err := a.Store.RepoBySlug(r.Context(), r.PathValue("slug"))
		if err != nil {
			errJSON(w, 404, "not found")
			return
		}
		m, err := a.Store.MirrorByRepo(r.Context(), repo.ID)
		if err != nil {
			mirrorErr(w, err)
			return
		}
		writeJSON(w, 200, toMirrorJSON(m))
	})
	mux.HandleFunc("PUT /api/repos/{slug}/mirror", a.put)
	mux.HandleFunc("DELETE /api/repos/{slug}/mirror", func(w http.ResponseWriter, r *http.Request) {
		repo, err := a.Store.RepoBySlug(r.Context(), r.PathValue("slug"))
		if err != nil {
			errJSON(w, 404, "not found")
			return
		}
		if err := a.Store.DeleteMirror(r.Context(), repo.ID); err != nil {
			mirrorErr(w, err)
			return
		}
		a.audit(r, repo.ID, "mirror-removed", "")
		writeJSON(w, 200, map[string]string{"status": "deleted"})
	})
	// Sync writes are attributed to mirror:<slug> (as scheduled runs are); the admin who
	// asked for the run is recorded as a sync-requested event.
	mux.HandleFunc("POST /api/repos/{slug}/mirror/sync", func(w http.ResponseWriter, r *http.Request) {
		repo, err := a.Store.RepoBySlug(r.Context(), r.PathValue("slug"))
		if err != nil {
			errJSON(w, 404, "not found")
			return
		}
		if err := a.Syncer.Trigger(r.Context(), repo.ID, "mirror:"+repo.Slug); err != nil {
			mirrorErr(w, err)
			return
		}
		a.audit(r, repo.ID, "sync-requested", "")
		writeJSON(w, 202, map[string]string{"status": "started"})
	})
	mux.HandleFunc("GET /api/repos/{slug}/batches", func(w http.ResponseWriter, r *http.Request) {
		repo, err := a.Store.RepoBySlug(r.Context(), r.PathValue("slug"))
		if err != nil {
			errJSON(w, 404, "not found")
			return
		}
		bs, err := a.Store.ListBatches(r.Context(), repo.ID)
		if err != nil {
			errJSON(w, 500, "internal error")
			return
		}
		out := []map[string]any{}
		for _, b := range bs {
			out = append(out, map[string]any{"id": b.ID, "started_at": ts(&b.StartedAt), "finished_at": ts(b.FinishedAt),
				"status": b.Status, "added": b.AddedCount, "error": b.Error})
		}
		writeJSON(w, 200, out)
	})
}

func mirrorErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		errJSON(w, 404, "not a mirror")
	case errors.Is(err, store.ErrBusy), errors.Is(err, store.ErrNotEligible):
		errJSON(w, 409, err.Error())
	default:
		log.Printf("mirror api: %v", err)
		errJSON(w, 500, "internal error")
	}
}

func (a *Mirrors) audit(r *http.Request, repoID int64, action, detail string) {
	if err := a.Store.AddAudit(r.Context(), repoID, a.Actor(r), action, detail); err != nil {
		log.Printf("mirror api: audit %s: %v", action, err)
	}
}

// put creates or replaces the mirror config. Every field is replaced except the
// credentials: an omitted token (and username) keeps the stored one. Error
// messages never echo request values, so a token can't leak back through them.
func (a *Mirrors) put(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	repo, err := a.Store.RepoBySlug(ctx, r.PathValue("slug"))
	if err != nil {
		errJSON(w, 404, "not found")
		return
	}
	var in struct {
		UpstreamURL      string            `json:"upstream_url"`
		RepoFile         string            `json:"repo_file"`
		Section          string            `json:"section"`
		Vars             map[string]string `json:"vars"`
		Username         *string           `json:"username"`
		Token            *string           `json:"token"`
		ClearCredentials bool              `json:"clear_credentials"`
		GPGKey           string            `json:"gpg_key"`
		RequireSignature bool              `json:"require_signature"`
		Arches           []string          `json:"arches"`
		Include          []string          `json:"include"`
		Exclude          []string          `json:"exclude"`
		KeepLatest       int               `json:"keep_latest"`
		Schedule         scheduleJSON      `json:"schedule"`
	}
	if !decode(w, r, &in) {
		return
	}
	if (in.UpstreamURL == "") == (in.RepoFile == "") {
		errJSON(w, 400, "give exactly one of upstream_url or repo_file")
		return
	}

	// upstream URL, gpgkey URL and credentials from the URL or the chosen .repo section
	rawURL, kind, rawKey, user, pass, requireSig := in.UpstreamURL, "baseurl", "", "", "", in.RequireSignature
	if in.RepoFile != "" {
		secs, err := mirror.ParseRepoFile(in.RepoFile)
		if err != nil {
			errJSON(w, 400, err.Error())
			return
		}
		ids := []string{}
		for _, s := range secs {
			ids = append(ids, s.ID)
		}
		i := slices.IndexFunc(secs, func(s mirror.RepoSection) bool { return s.ID == in.Section })
		if in.Section == "" && len(secs) == 1 {
			i = 0
		}
		if i < 0 {
			writeJSON(w, 400, map[string]any{"error": "choose one section of the .repo file", "sections": ids})
			return
		}
		s := secs[i]
		rawURL, kind = sectionURL(s)
		rawKey, user, pass, requireSig = keyURL(s), s.Username, s.Password, requireSig || s.RepoGPGCheck
	}
	u, missing := mirror.ResolveVars(rawURL, in.Vars)
	keyU, missingKey := mirror.ResolveVars(rawKey, in.Vars)
	missing = append(missing, missingKey...)
	slices.Sort(missing)
	if missing = slices.Compact(missing); len(missing) > 0 {
		writeJSON(w, 400, map[string]any{"error": "values needed for variables: " + strings.Join(missing, ", "), "missing_vars": missing})
		return
	}
	if !httpURL(u) {
		errJSON(w, 400, "upstream URL must be http or https, with credentials in username/token rather than the URL")
		return
	}

	sc := store.Schedule{Frequency: in.Schedule.Frequency, TimeOfDay: in.Schedule.TimeOfDay, Timezone: in.Schedule.Timezone,
		Weekdays: in.Schedule.Weekdays, DayOfMonth: in.Schedule.DayOfMonth, Paused: in.Schedule.Paused}
	if sc.Frequency == "" {
		sc.Frequency = "manual"
	}
	if err := mirror.ValidateSchedule(sc); err != nil {
		errJSON(w, 400, "schedule: "+err.Error())
		return
	}
	if in.KeepLatest < 0 {
		errJSON(w, 400, "keep_latest must be 0 (all) or more")
		return
	}
	for _, g := range slices.Concat(in.Arches, in.Include, in.Exclude) {
		if _, err := path.Match(g, ""); err != nil || g == "" {
			errJSON(w, 400, "invalid pattern in arches/include/exclude")
			return
		}
	}

	// credentials: start from what is stored, clear if asked, then apply new values
	old, err := a.Store.MirrorByRepo(ctx, repo.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		mirrorErr(w, err)
		return
	}
	username, tokenEnc := old.Username, old.TokenEnc
	if in.ClearCredentials {
		username, tokenEnc = "", nil
	}
	if user != "" {
		username = user
	}
	if in.Username != nil {
		username = *in.Username
	}
	if in.Token != nil {
		pass = *in.Token
		if pass == "" {
			tokenEnc = nil
		}
	}
	if a.Box == nil && pass == "" && len(tokenEnc) > 0 {
		errJSON(w, 400, "keeping the stored upstream credentials needs SECRET_KEY to be set on the server; send clear_credentials to drop them")
		return
	}
	if pass != "" {
		if a.Box == nil {
			errJSON(w, 400, "upstream credentials need SECRET_KEY to be set on the server")
			return
		}
		if tokenEnc, err = a.Box.Seal(pass); err != nil {
			errJSON(w, 500, "internal error")
			return
		}
	}

	err = nil
	switch kind { // network calls last, after every cheap check
	case "metalink":
		u, err = mirror.ResolveMetalink(ctx, a.client(), u)
	case "mirrorlist":
		u, err = mirror.ResolveMirrorList(ctx, a.client(), u)
	}
	if err != nil {
		errJSON(w, 400, "resolve "+kind+": "+err.Error()) // errors carry only the redacted URL
		return
	}
	if !httpURL(u) {
		errJSON(w, 400, "resolved "+kind+" URL must be http or https without embedded credentials")
		return
	}
	key := in.GPGKey
	if key == "" && keyU != "" {
		if key, err = mirror.FetchKey(ctx, a.client(), keyU); err != nil {
			errJSON(w, 400, "fetch gpgkey: "+err.Error())
			return
		}
	}
	if key != "" && !strings.Contains(key, "-----BEGIN PGP PUBLIC KEY BLOCK-----") {
		errJSON(w, 400, "gpg_key must be an ASCII-armored public key")
		return
	}
	if requireSig && key == "" {
		errJSON(w, 400, "require_signature (or repo_gpgcheck=1) needs a gpg_key")
		return
	}
	next, err := mirror.NextRun(sc, time.Now()) // stored so saving a schedule never triggers an immediate run
	if err != nil {
		errJSON(w, 400, "schedule: "+err.Error())
		return
	}

	m := store.Mirror{RepoID: repo.ID, UpstreamURL: u, Username: username, TokenEnc: tokenEnc, GPGKey: key, RequireSig: requireSig,
		Arches: in.Arches, Include: in.Include, Exclude: in.Exclude, KeepLatest: in.KeepLatest, Schedule: sc, NextRun: next}
	if err := a.Store.SetMirror(ctx, m); err != nil {
		mirrorErr(w, err)
		return
	}
	a.audit(r, repo.ID, "mirror-configured", mirror.Redact(u))
	saved, err := a.Store.MirrorByRepo(ctx, repo.ID)
	if err != nil {
		mirrorErr(w, err)
		return
	}
	writeJSON(w, 200, toMirrorJSON(saved))
}
