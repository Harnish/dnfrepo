// Package registry embeds the Distribution container registry behind dnfrepo's access rules.
package registry

import (
	"context"
	"net/http"
	"net/netip"

	"dnfrepo/internal/access"
	"dnfrepo/internal/store"
)

// PolicyStore is the subset of store.Store the policy needs.
type PolicyStore interface {
	RegistryRepoByName(ctx context.Context, name string) (store.RegistryRepo, error)
	EnsureRegistryRepo(ctx context.Context, name string) error
	RegistryIPs(ctx context.Context, name string) ([]netip.Prefix, error)
	CheckRegistryUser(ctx context.Context, name, user, token string) (bool, error)
	CheckPushToken(ctx context.Context, user, token string) (bool, error)
}

// Policy decides who may do what to which image.
type Policy struct {
	Store    PolicyStore
	Trusted  []netip.Prefix
	Throttle *access.Throttle
	// Exists reports whether an image has content in storage. A name with no settings row is
	// public (the default) only while it exists; nil means nothing exists.
	Exists func(ctx context.Context, name string) bool
}

// Caller is who a request claims to be. User is only a claim until CanPull checks it per image.
type Caller struct {
	Anonymous bool
	Push      bool
	User      string
}

// Access is one requested action on one image: pull, push, delete or catalog.
type Access struct{ Repo, Action string }

type Verdict int

const (
	Allow     Verdict = iota
	Challenge         // 401 with a Basic challenge; identical for "denied" and "does not exist"
	Throttled         // 429
	Error             // 500
)

// Identify classifies a request. Credentialed requests are counted against the throttle before
// they are checked; a correct push token resets the count.
func (p *Policy) Identify(r *http.Request) (Caller, Verdict) {
	user, pass, ok := r.BasicAuth()
	if !ok {
		return Caller{Anonymous: true}, Allow
	}
	ip := access.ClientIP(r, p.Trusted)
	if !p.Throttle.Attempt(ip) {
		return Caller{}, Throttled
	}
	good, err := p.Store.CheckPushToken(r.Context(), user, pass)
	if err != nil {
		return Caller{}, Error
	}
	if good {
		p.Throttle.Reset(ip)
		return Caller{Push: true, User: user}, Allow
	}
	return Caller{User: user}, Allow
}

func inAny(a netip.Addr, ps []netip.Prefix) bool {
	for _, p := range ps {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// CanPull reports whether c may read image repo.
func (p *Policy) CanPull(r *http.Request, c Caller, repo string) (bool, error) {
	if c.Push {
		return true, nil
	}
	row, err := p.Store.RegistryRepoByName(r.Context(), repo)
	if err != nil {
		return false, err
	}
	if !row.Exists && (p.Exists == nil || !p.Exists(r.Context(), repo)) {
		return false, nil
	}
	switch row.Mode {
	case store.ModePublic:
		return true, nil
	case store.ModeIPs:
		ips, err := p.Store.RegistryIPs(r.Context(), repo)
		if err != nil {
			return false, err
		}
		return inAny(access.ClientIP(r, p.Trusted), ips), nil
	case store.ModeUsers:
		user, pass, ok := r.BasicAuth()
		if !ok || c.Anonymous { // public endpoints never test credentials
			return false, nil
		}
		good, err := p.Store.CheckRegistryUser(r.Context(), repo, user, pass)
		if good {
			p.Throttle.Reset(access.ClientIP(r, p.Trusted))
		}
		return good, err
	}
	return false, nil
}

// Authorize decides a request that asks for accesses. Delete is never allowed over HTTP,
// push needs a push token, pull follows the image's access mode, and the catalog is open
// (the catalog handler filters what it lists).
func (p *Policy) Authorize(r *http.Request, accesses []Access) Verdict {
	c, v := p.Identify(r)
	if v != Allow {
		return v
	}
	for _, a := range accesses {
		switch a.Action {
		case "catalog":
		case "pull":
			ok, err := p.CanPull(r, c, a.Repo)
			if err != nil {
				return Error
			}
			if !ok {
				return Challenge
			}
		case "push":
			if !c.Push {
				return Challenge
			}
			if err := p.Store.EnsureRegistryRepo(r.Context(), a.Repo); err != nil {
				return Error
			}
		default: // delete and anything unknown
			return Challenge
		}
	}
	p.release(r, c)
	return Allow
}

// release gives back the throttle count of an allowed request whose credentials were not what
// allowed it (a push token or a granted user earned its pass and was already reset). Without
// this a logged-in client pulling public images would exhaust its own allowance.
func (p *Policy) release(r *http.Request, c Caller) {
	if _, _, ok := r.BasicAuth(); ok && !c.Push {
		p.Throttle.Refund(access.ClientIP(r, p.Trusted))
	}
}
