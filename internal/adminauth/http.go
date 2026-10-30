package adminauth

import (
	"encoding/json"
	"net/http"
	"strings"

	"dnfrepo/internal/access"
)

// decodeBody reads a small JSON body (64 KiB max).
func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(v) != nil {
		errJSON(w, http.StatusBadRequest, "bad json")
		return false
	}
	return true
}

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

// PublicRoutes registers the endpoints usable without a session.
func (s *Service) PublicRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		ip := access.ClientIP(r, s.trusted)
		if s.th.Blocked(ip) {
			errJSON(w, 429, "too many failed attempts; try again later")
			return
		}
		var in struct{ Email, Password string }
		if !decodeBody(w, r, &in) {
			return
		}
		tok, err := s.Login(r.Context(), in.Email, in.Password)
		if err != nil {
			s.th.Fail(ip)
			errJSON(w, 401, err.Error())
			return
		}
		http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: tok, Path: "/", HttpOnly: true,
			SameSite: http.SameSiteLaxMode, Secure: strings.HasPrefix(s.baseURL, "https://"), MaxAge: int(sessionTTL.Seconds())})
		writeJSON(w, 200, map[string]string{"email": strings.ToLower(in.Email)})
	})
	mux.HandleFunc("POST /api/auth/verify", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Token, Password string }
		if !decodeBody(w, r, &in) {
			return
		}
		if err := s.VerifyAndSetPassword(r.Context(), in.Token, in.Password); err != nil {
			errJSON(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, map[string]string{"status": "verified"})
	})
}

// AdminRoutes registers the endpoints that require a session (wrap with RequireAdmin).
func (s *Service) AdminRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(SessionCookie); err == nil {
			s.Logout(r.Context(), c.Value)
		}
		http.SetCookie(w, &http.Cookie{Name: SessionCookie, Path: "/", MaxAge: -1})
		writeJSON(w, 200, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /api/auth/me", func(w http.ResponseWriter, r *http.Request) {
		email, _ := s.Authenticate(r)
		writeJSON(w, 200, map[string]string{"email": email})
	})
	mux.HandleFunc("POST /api/admins/invite", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Email string }
		if !decodeBody(w, r, &in) {
			return
		}
		actor, _ := s.Authenticate(r)
		if err := s.Invite(r.Context(), actor, in.Email); err != nil {
			errJSON(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, map[string]string{"status": "invited"})
	})
}
