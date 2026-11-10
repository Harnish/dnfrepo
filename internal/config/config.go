// Package config loads service settings from environment variables.
package config

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ListenAddr, DataDir, DatabaseURL, BaseURL string
	TrustedProxies                            []netip.Prefix
	OIDCIssuer, OIDCClientID                  string
	OIDCClientSecret, OIDCAdminGroup          string
	GitHubToken                               string
	AdminEmail, AdminPassword                 string // optional local admin login (homelab); replaces OIDC when set
	MaxUploadBytes                            int64
	SecretKey                                 string        // optional; seals upstream credentials
	RebuildTimeout                            time.Duration // metadata rebuild timeout
}

func Load(getenv func(string) string) (Config, error) {
	def := func(k, d string) string {
		if v := getenv(k); v != "" {
			return v
		}
		return d
	}
	c := Config{
		ListenAddr:       def("LISTEN_ADDR", ":8080"),
		DataDir:          def("DATA_DIR", "/data"),
		DatabaseURL:      getenv("DATABASE_URL"),
		BaseURL:          strings.TrimRight(getenv("BASE_URL"), "/"),
		OIDCIssuer:       getenv("OIDC_ISSUER"),
		OIDCClientID:     getenv("OIDC_CLIENT_ID"),
		OIDCClientSecret: getenv("OIDC_CLIENT_SECRET"),
		OIDCAdminGroup:   def("OIDC_ADMIN_GROUP", "dnfrepo-admins"),
		GitHubToken:      getenv("GITHUB_TOKEN"),
		AdminEmail:       strings.ToLower(strings.TrimSpace(getenv("ADMIN_EMAIL"))),
		AdminPassword:    getenv("ADMIN_PASSWORD"),
		MaxUploadBytes:   1 << 30,
		SecretKey:        getenv("SECRET_KEY"),
		RebuildTimeout:   10 * time.Minute,
	}
	if c.DatabaseURL == "" {
		return c, fmt.Errorf("DATABASE_URL required")
	}
	if c.BaseURL == "" {
		return c, fmt.Errorf("BASE_URL required")
	}
	if (c.AdminEmail == "") != (c.AdminPassword == "") {
		return c, fmt.Errorf("ADMIN_EMAIL and ADMIN_PASSWORD must be set together")
	}
	for k, v := range map[string]string{"OIDC_ISSUER": c.OIDCIssuer, "OIDC_CLIENT_ID": c.OIDCClientID, "OIDC_CLIENT_SECRET": c.OIDCClientSecret} {
		if v == "" && c.AdminEmail == "" { // local admin login replaces OIDC
			return c, fmt.Errorf("%s required", k)
		}
	}
	if v := getenv("MAX_UPLOAD_BYTES"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return c, fmt.Errorf("MAX_UPLOAD_BYTES: %w", err)
		}
		c.MaxUploadBytes = n
	}
	if v := getenv("REBUILD_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return c, fmt.Errorf("REBUILD_TIMEOUT: %w", err)
		}
		c.RebuildTimeout = d
	}
	for _, p := range strings.Split(getenv("TRUSTED_PROXIES"), ",") {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		pf, err := netip.ParsePrefix(p)
		if err != nil {
			a, err2 := netip.ParseAddr(p)
			if err2 != nil {
				return c, fmt.Errorf("TRUSTED_PROXIES %q: %w", p, err)
			}
			pf = netip.PrefixFrom(a, a.BitLen())
		}
		c.TrustedProxies = append(c.TrustedProxies, pf)
	}
	return c, nil
}
