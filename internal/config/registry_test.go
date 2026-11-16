package config

import "testing"

func TestLoadRegistry(t *testing.T) {
	base := map[string]string{"DATABASE_URL": "p", "BASE_URL": "https://dnf.example.org/", "ADMIN_EMAIL": "a@x", "ADMIN_PASSWORD": "p"}
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
	c, err := Load(env(with()))
	if err != nil || c.RegistryDir != "" {
		t.Fatalf("default: %+v %v", c, err)
	}
	c, err = Load(env(with("REGISTRY_DIR", "/registry")))
	if err != nil || c.RegistryDir != "/registry" || c.RegistryHost != "dnf.example.org" {
		t.Fatalf("dir set: %+v %v", c, err)
	}
	c, err = Load(env(with("REGISTRY_DIR", "/registry", "REGISTRY_HOST", "registry.example.org")))
	if err != nil || c.RegistryHost != "registry.example.org" {
		t.Fatalf("host override: %+v %v", c, err)
	}
}
