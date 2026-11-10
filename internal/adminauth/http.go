package adminauth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"slices"
	"strings"

	"dnfrepo/internal/access"
)

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func errJSON(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// RequireAdmin rejects requests without a valid admin session (401 JSON).
func (s *Service) RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.Authenticate(r); !ok {
			errJSON(w, http.StatusUnauthorized, "login required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// CSRF requires X-Requested-With: dnfrepo on state-changing requests.
func CSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			if r.Header.Get("X-Requested-With") != "dnfrepo" {
				errJSON(w, http.StatusForbidden, "missing X-Requested-With header")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// PublicRoutes registers the OIDC login endpoints, usable without a session.
func (s *Service) PublicRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/auth/methods", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]bool{"local": s.hasLocal})
	})
	if s.hasLocal {
		mux.HandleFunc("POST /api/auth/local", s.localLogin)
	}
	mux.HandleFunc("GET /api/auth/oidc/login", func(w http.ResponseWriter, r *http.Request) {
		state, nonce, verifier := randToken(), randToken(), randToken()
		sum := sha256.Sum256([]byte(verifier))
		to, err := s.idp.AuthURL(state, nonce, base64.RawURLEncoding.EncodeToString(sum[:]))
		if err != nil {
			log.Printf("oidc discovery: %v", err)
			errJSON(w, http.StatusServiceUnavailable, "identity provider unavailable; try again shortly")
			return
		}
		http.SetCookie(w, s.cookie(oidcCookie, state+"."+nonce+"."+verifier, int(flowTTL.Seconds())))
		http.Redirect(w, r, to, http.StatusFound)
	})
	mux.HandleFunc("GET /api/auth/oidc/callback", func(w http.ResponseWriter, r *http.Request) {
		ip := access.ClientIP(r, s.trusted)
		if !s.th.Attempt(ip) { // counted before any checking so parallel bursts can't outrun it
			errJSON(w, http.StatusTooManyRequests, "too many attempts; try again later")
			return
		}
		c, err := r.Cookie(oidcCookie)
		http.SetCookie(w, s.cookie(oidcCookie, "", -1))
		var flow []string
		if err == nil {
			flow = strings.Split(c.Value, ".")
		}
		if len(flow) != 3 || subtle.ConstantTimeCompare([]byte(flow[0]), []byte(r.URL.Query().Get("state"))) != 1 {
			errJSON(w, http.StatusBadRequest, "invalid or expired login; start again")
			return
		}
		claims, err := s.idp.Exchange(r.Context(), r.URL.Query().Get("code"), flow[2], flow[1])
		if err != nil {
			log.Printf("oidc exchange: %v", err) // error text can echo provider detail; never sent to the client
			errJSON(w, http.StatusUnauthorized, "login failed")
			return
		}
		email := strings.ToLower(strings.TrimSpace(claims.Email))
		if email == "" || !slices.Contains(claims.Groups, s.group) {
			errJSON(w, http.StatusForbidden, "not a member of the admin group")
			return
		}
		tok, err := s.newSession(r.Context(), email)
		if err != nil {
			errJSON(w, http.StatusServiceUnavailable, "try again")
			return
		}
		s.th.Reset(ip)
		http.SetCookie(w, s.cookie(SessionCookie, tok, int(sessionTTL.Seconds())))
		http.Redirect(w, r, "/", http.StatusFound)
	})
}

// AdminRoutes registers the endpoints that require a session (wrap with RequireAdmin).
func (s *Service) AdminRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(SessionCookie); err == nil {
			s.Logout(r.Context(), c.Value)
		}
		http.SetCookie(w, s.cookie(SessionCookie, "", -1))
		writeJSON(w, 200, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /api/auth/me", func(w http.ResponseWriter, r *http.Request) {
		email, _ := s.Authenticate(r)
		writeJSON(w, 200, map[string]string{"email": email})
	})
}

// localLogin signs in the single ADMIN_EMAIL/ADMIN_PASSWORD admin. Same throttle and session as OIDC.
func (s *Service) localLogin(w http.ResponseWriter, r *http.Request) {
	ip := access.ClientIP(r, s.trusted)
	if !s.th.Attempt(ip) { // counted before checking
		errJSON(w, http.StatusTooManyRequests, "too many attempts; try again later")
		return
	}
	var b struct{ Email, Password string }
	json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&b)
	email := strings.ToLower(strings.TrimSpace(b.Email))
	e, p := sha256.Sum256([]byte(email)), sha256.Sum256([]byte(b.Password))
	// hashes compared in constant time; both compared so a wrong email isn't distinguishable by timing
	okE, okP := subtle.ConstantTimeCompare(e[:], s.local[0][:]), subtle.ConstantTimeCompare(p[:], s.local[1][:])
	if okE&okP != 1 {
		errJSON(w, http.StatusUnauthorized, "invalid email or password")
		return
	}
	tok, err := s.newSession(r.Context(), email)
	if err != nil {
		errJSON(w, http.StatusServiceUnavailable, "try again")
		return
	}
	s.th.Reset(ip)
	http.SetCookie(w, s.cookie(SessionCookie, tok, int(sessionTTL.Seconds())))
	writeJSON(w, 200, map[string]string{"status": "ok"})
}
