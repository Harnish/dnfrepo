package config

import (
	"net/netip"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLoadRequiresDatabaseURLAndBaseURL(t *testing.T) {
	_, err := Load(env(map[string]string{"BASE_URL": "http://x"}))
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("want DATABASE_URL error, got %v", err)
	}
	_, err = Load(env(map[string]string{"DATABASE_URL": "postgres://x", "OIDC_ISSUER": "i", "OIDC_CLIENT_ID": "c", "OIDC_CLIENT_SECRET": "s"}))
	if err == nil || !strings.Contains(err.Error(), "BASE_URL") {
		t.Fatalf("want BASE_URL error, got %v", err)
	}
}

func TestLoadOIDC(t *testing.T) {
	base := map[string]string{"DATABASE_URL": "p", "BASE_URL": "http://x"}
	with := func(kv ...string) map[string]string {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	for _, miss := range []string{"OIDC_ISSUER", "OIDC_CLIENT_ID", "OIDC_CLIENT_SECRET"} {
		m := with("OIDC_ISSUER", "https://a/", "OIDC_CLIENT_ID", "id", "OIDC_CLIENT_SECRET", "s")
		delete(m, miss)
		if _, err := Load(env(m)); err == nil || !strings.Contains(err.Error(), miss) {
			t.Fatalf("missing %s: got %v", miss, err)
		}
	}
	c, err := Load(env(with("OIDC_ISSUER", "https://a/", "OIDC_CLIENT_ID", "id", "OIDC_CLIENT_SECRET", "s")))
	if err != nil || c.OIDCAdminGroup != "dnfrepo-admins" || c.OIDCIssuer != "https://a/" {
		t.Fatalf("defaults: %+v %v", c, err)
	}
	c, _ = Load(env(with("OIDC_ISSUER", "i", "OIDC_CLIENT_ID", "id", "OIDC_CLIENT_SECRET", "s", "OIDC_ADMIN_GROUP", "ops")))
	if c.OIDCAdminGroup != "ops" {
		t.Fatalf("group override: %q", c.OIDCAdminGroup)
	}
}

func TestLoadParsesTrustedProxies(t *testing.T) {
	c, err := Load(env(map[string]string{"DATABASE_URL": "p", "BASE_URL": "http://x", "OIDC_ISSUER": "i", "OIDC_CLIENT_ID": "c", "OIDC_CLIENT_SECRET": "s", "TRUSTED_PROXIES": "10.0.0.0/8,192.168.1.5"}))
	if err != nil {
		t.Fatal(err)
	}
	want := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("192.168.1.5/32")}
	if len(c.TrustedProxies) != 2 || c.TrustedProxies[0] != want[0] || c.TrustedProxies[1] != want[1] {
		t.Fatalf("got %v", c.TrustedProxies)
	}
}

func TestLoadLocalAdmin(t *testing.T) {
	base := map[string]string{"DATABASE_URL": "p", "BASE_URL": "http://x"}
	with := func(kv ...string) map[string]string {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	c, err := Load(env(with("ADMIN_EMAIL", "a@b.c", "ADMIN_PASSWORD", "pw")))
	if err != nil || c.AdminEmail != "a@b.c" || c.AdminPassword != "pw" {
		t.Fatalf("local-only: %+v %v", c, err)
	}
	if _, err := Load(env(with("ADMIN_EMAIL", "a@b.c"))); err == nil {
		t.Fatal("email without password accepted")
	}
	if _, err := Load(env(with("ADMIN_PASSWORD", "pw"))); err == nil {
		t.Fatal("password without email accepted")
	}
}

func TestLoadSecretKeyAndRebuildTimeout(t *testing.T) {
	m := map[string]string{"DATABASE_URL": "p", "BASE_URL": "http://x", "OIDC_ISSUER": "i", "OIDC_CLIENT_ID": "c", "OIDC_CLIENT_SECRET": "s"}
	c, err := Load(env(m))
	if err != nil || c.RebuildTimeout != 10*time.Minute || c.SecretKey != "" {
		t.Fatalf("defaults: %+v %v", c, err)
	}
	m["REBUILD_TIMEOUT"], m["SECRET_KEY"] = "30m", "k"
	c, err = Load(env(m))
	if err != nil || c.RebuildTimeout != 30*time.Minute || c.SecretKey != "k" {
		t.Fatalf("override: %+v %v", c, err)
	}
	m["REBUILD_TIMEOUT"] = "abc"
	if _, err := Load(env(m)); err == nil {
		t.Fatal("invalid duration accepted")
	}
}

func TestLoadMirrorMaxRPMBytes(t *testing.T) {
	m := map[string]string{"DATABASE_URL": "p", "BASE_URL": "http://x", "OIDC_ISSUER": "i", "OIDC_CLIENT_ID": "c", "OIDC_CLIENT_SECRET": "s"}
	c, err := Load(env(m))
	if err != nil || c.MirrorMaxRPMBytes != 4<<30 {
		t.Fatalf("default: %d %v", c.MirrorMaxRPMBytes, err)
	}
	m["MIRROR_MAX_RPM_BYTES"] = "123"
	if c, err = Load(env(m)); err != nil || c.MirrorMaxRPMBytes != 123 {
		t.Fatalf("override: %d %v", c.MirrorMaxRPMBytes, err)
	}
	m["MIRROR_MAX_RPM_BYTES"] = "x"
	if _, err := Load(env(m)); err == nil {
		t.Fatal("invalid size accepted")
	}
}
