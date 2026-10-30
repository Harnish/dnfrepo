// Package config loads service settings from environment variables.
package config

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

type Config struct {
	ListenAddr, DataDir, DatabaseURL, BaseURL string
	TrustedProxies                            []netip.Prefix
	SMTPHost                                  string
	SMTPPort                                  int
	SMTPUser, SMTPPass, SMTPFrom              string
	BootstrapAdminEmail, GitHubToken          string
	MaxUploadBytes                            int64
}

func Load(getenv func(string) string) (Config, error) {
	def := func(k, d string) string {
		if v := getenv(k); v != "" {
			return v
		}
		return d
	}
	c := Config{
		ListenAddr:          def("LISTEN_ADDR", ":8080"),
		DataDir:             def("DATA_DIR", "/data"),
		DatabaseURL:         getenv("DATABASE_URL"),
		BaseURL:             strings.TrimRight(getenv("BASE_URL"), "/"),
		SMTPHost:            getenv("SMTP_HOST"),
		SMTPUser:            getenv("SMTP_USER"),
		SMTPPass:            getenv("SMTP_PASS"),
		SMTPFrom:            getenv("SMTP_FROM"),
		BootstrapAdminEmail: getenv("BOOTSTRAP_ADMIN_EMAIL"),
		GitHubToken:         getenv("GITHUB_TOKEN"),
		SMTPPort:            587,
		MaxUploadBytes:      1 << 30,
	}
	if c.DatabaseURL == "" {
		return c, fmt.Errorf("DATABASE_URL required")
	}
	if c.BaseURL == "" {
		return c, fmt.Errorf("BASE_URL required")
	}
	if v := getenv("SMTP_PORT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return c, fmt.Errorf("SMTP_PORT: %w", err)
		}
		c.SMTPPort = n
	}
	if v := getenv("MAX_UPLOAD_BYTES"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return c, fmt.Errorf("MAX_UPLOAD_BYTES: %w", err)
		}
		c.MaxUploadBytes = n
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
