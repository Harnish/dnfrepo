package config

import (
	"net/netip"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLoadRequiresDatabaseURLAndBaseURL(t *testing.T) {
	_, err := Load(env(map[string]string{"BASE_URL": "http://x"}))
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("want DATABASE_URL error, got %v", err)
	}
	_, err = Load(env(map[string]string{"DATABASE_URL": "postgres://x"}))
	if err == nil || !strings.Contains(err.Error(), "BASE_URL") {
		t.Fatalf("want BASE_URL error, got %v", err)
	}
}

func TestLoadParsesTrustedProxies(t *testing.T) {
	c, err := Load(env(map[string]string{"DATABASE_URL": "p", "BASE_URL": "http://x", "TRUSTED_PROXIES": "10.0.0.0/8,192.168.1.5"}))
	if err != nil {
		t.Fatal(err)
	}
	want := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("192.168.1.5/32")}
	if len(c.TrustedProxies) != 2 || c.TrustedProxies[0] != want[0] || c.TrustedProxies[1] != want[1] {
		t.Fatalf("got %v", c.TrustedProxies)
	}
}
