package config

import "testing"

func TestLoadDedup(t *testing.T) {
	base := map[string]string{"DATABASE_URL": "p", "BASE_URL": "http://x", "ADMIN_EMAIL": "a@x", "ADMIN_PASSWORD": "p"}
	with := func(v string) map[string]string {
		m := map[string]string{"DEDUP": v}
		for k, x := range base {
			m[k] = x
		}
		return m
	}
	if c, err := Load(env(with(""))); err != nil || c.Dedup {
		t.Fatalf("default: %+v %v", c, err)
	}
	if c, err := Load(env(with("true"))); err != nil || !c.Dedup {
		t.Fatalf("true: %+v %v", c, err)
	}
	if _, err := Load(env(with("maybe"))); err == nil {
		t.Fatal("bad DEDUP accepted")
	}
}
