package registry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"

	"github.com/distribution/distribution/v3"
	"github.com/distribution/distribution/v3/configuration"
	"github.com/distribution/distribution/v3/registry/handlers"
	"github.com/distribution/distribution/v3/registry/storage"
	"github.com/distribution/distribution/v3/registry/storage/driver"
	"github.com/distribution/distribution/v3/registry/storage/driver/factory"
	_ "github.com/distribution/distribution/v3/registry/storage/driver/filesystem"
	"github.com/sirupsen/logrus"
)

// Config configures New. Root is the registry data directory (filesystem driver).
type Config struct {
	Root   string
	Policy *Policy
}

// Registry is an embedded Distribution registry whose access control is a Policy.
type Registry struct {
	app    *handlers.App
	ns     distribution.Namespace
	drv    driver.StorageDriver
	policy *Policy
	gate   *Gate
	gc     gcState
}

// New builds the registry. It sets c.Policy.Exists from the storage layout.
func New(ctx context.Context, c Config) (reg *Registry, err error) {
	if c.Root == "" || c.Policy == nil {
		return nil, fmt.Errorf("registry: Root and Policy are required")
	}
	registerController()
	secret := make([]byte, 32)
	rand.Read(secret)
	cfg := &configuration.Configuration{}
	cfg.Storage = configuration.Storage{
		"filesystem": configuration.Parameters{"rootdirectory": c.Root},
		"delete":     configuration.Parameters{"enabled": true},
	}
	cfg.Auth = configuration.Auth{authName: configuration.Parameters{"policy": c.Policy}}
	cfg.HTTP.Secret = hex.EncodeToString(secret) // signs upload state; per process is fine for one replica
	cfg.Log.AccessLog.Disabled = true
	logrus.SetLevel(logrus.ErrorLevel) // distribution logs every request at info and every 401 at warn

	defer func() { // NewApp panics on a bad configuration
		if r := recover(); r != nil {
			reg, err = nil, fmt.Errorf("registry: %v", r)
		}
	}()
	drv, err := factory.Create(ctx, "filesystem", map[string]any{"rootdirectory": c.Root})
	if err != nil {
		return nil, err
	}
	ns, err := storage.NewRegistry(ctx, drv, storage.EnableDelete)
	if err != nil {
		return nil, err
	}
	r := &Registry{app: handlers.NewApp(ctx, cfg), ns: ns, drv: drv, policy: c.Policy, gate: &Gate{}}
	c.Policy.Exists = r.exists
	return r, nil
}

// exists reports whether image name has registry content (a _manifests directory).
func (r *Registry) exists(ctx context.Context, name string) bool {
	fi, err := r.drv.Stat(ctx, "/docker/registry/v2/repositories/"+name+"/_manifests")
	return err == nil && fi.IsDir()
}

// Handler is the raw distribution app (no catalog filtering, no write gate).
func (r *Registry) Handler() http.Handler { return r.app }

func (r *Registry) Namespace() distribution.Namespace { return r.ns }
func (r *Registry) Driver() driver.StorageDriver      { return r.drv }
