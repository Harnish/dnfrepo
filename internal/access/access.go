// Package access enforces each repo's access mode (public, ips, or users).
package access

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strings"

	"dnfrepo/internal/store"
)

// RepoStore is the subset of store.Store the guard needs.
type RepoStore interface {
	RepoBySlug(ctx context.Context, slug string) (store.Repo, error)
	RepoIPs(ctx context.Context, id int64) ([]netip.Prefix, error)
	CheckRepoToken(ctx context.Context, repoID int64, user, token string) (bool, error)
}

func inAny(a netip.Addr, ps []netip.Prefix) bool {
	for _, p := range ps {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// ClientIP is the TCP peer, or, when the peer is a trusted proxy, the right-most
// X-Forwarded-For hop that is not itself a trusted proxy.
func ClientIP(r *http.Request, trusted []netip.Prefix) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, _ := netip.ParseAddr(host)
	peer = peer.Unmap()
	if !inAny(peer, trusted) {
		return peer
	}
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			continue
		}
		a = a.Unmap()
		if !inAny(a, trusted) {
			return a
		}
	}
	return peer
}

// Guard wraps next with the access check for the repo named by slugOf(r).
func Guard(st RepoStore, trusted []netip.Prefix) func(slugOf func(*http.Request) string, next http.Handler) http.Handler {
	th := newThrottle()
	return func(slugOf func(*http.Request) string, next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			repo, err := st.RepoBySlug(r.Context(), slugOf(r))
			if err != nil {
				http.NotFound(w, r)
				return
			}
			switch repo.Mode {
			case store.ModePublic:
			case store.ModeIPs:
				ips, err := st.RepoIPs(r.Context(), repo.ID)
				if err != nil {
					http.Error(w, "internal error", 500)
					return
				}
				if !inAny(ClientIP(r, trusted), ips) {
					http.Error(w, "forbidden", http.StatusForbidden)
					return
				}
			case store.ModeUsers:
				ip := ClientIP(r, trusted)
				if th.blocked(ip) {
					http.Error(w, "too many failed attempts", http.StatusTooManyRequests)
					return
				}
				user, pass, ok := r.BasicAuth()
				if !ok {
					w.Header().Set("WWW-Authenticate", `Basic realm="dnfrepo"`)
					http.Error(w, "authentication required", http.StatusUnauthorized)
					return
				}
				good, err := st.CheckRepoToken(r.Context(), repo.ID, user, pass)
				if err != nil {
					http.Error(w, "internal error", 500)
					return
				}
				if !good {
					th.fail(ip)
					w.Header().Set("WWW-Authenticate", `Basic realm="dnfrepo"`)
					http.Error(w, "invalid credentials", http.StatusUnauthorized)
					return
				}
			default:
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
