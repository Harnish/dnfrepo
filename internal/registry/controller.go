package registry

import (
	"errors"
	"net/http"
	"sync"

	"github.com/distribution/distribution/v3/registry/auth"
)

const authName = "dnfrepo"

var registerOnce sync.Once

// registerController makes the controller available to the distribution config under authName.
// The *Policy travels in the auth options, so several registries can coexist (tests).
func registerController() {
	registerOnce.Do(func() {
		auth.Register(authName, func(opts map[string]any) (auth.AccessController, error) {
			p, ok := opts["policy"].(*Policy)
			if !ok || p == nil {
				return nil, errors.New("dnfrepo auth: policy option missing")
			}
			return controller{p}, nil
		})
	})
}

type controller struct{ p *Policy }

// challenge asks the client to authenticate. Distribution always answers a Challenge with 401.
type challenge struct{ throttled bool }

func (c challenge) Error() string { return "authentication required" }

func (c challenge) SetHeaders(_ *http.Request, w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="dnfrepo registry"`)
	if c.throttled {
		w.Header().Set("Retry-After", "300")
	}
}

func (c controller) Authorized(r *http.Request, records ...auth.Access) (*auth.Grant, error) {
	acc := make([]Access, 0, len(records))
	for _, a := range records {
		switch {
		case a.Type == "registry" && a.Name == "catalog":
			acc = append(acc, Access{Action: "catalog"})
		case a.Type == "repository":
			acc = append(acc, Access{Repo: a.Name, Action: a.Action})
		default:
			acc = append(acc, Access{Repo: a.Name, Action: "delete"}) // unknown resources are refused
		}
	}
	switch c.p.Authorize(r, acc) {
	case Allow:
		user, _, _ := r.BasicAuth()
		return &auth.Grant{User: auth.UserInfo{Name: user}}, nil
	case Challenge:
		return nil, challenge{}
	case Throttled:
		return nil, challenge{throttled: true}
	}
	return nil, errors.New("authorization failed") // distribution answers 400
}
