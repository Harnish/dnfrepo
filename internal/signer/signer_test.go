package signer

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
)

func verify(t *testing.T, pub string, data, sig []byte) error {
	t.Helper()
	kr, err := openpgp.ReadArmoredKeyRing(strings.NewReader(pub))
	if err != nil {
		t.Fatal(err)
	}
	_, err = openpgp.CheckArmoredDetachedSignature(kr, bytes.NewReader(data), bytes.NewReader(sig), nil)
	return err
}

func TestSignVerifyRoundTrip(t *testing.T) {
	s, err := LoadOrCreate(filepath.Join(t.TempDir(), "key.asc"))
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("<repomd/>")
	sig, err := s.SignDetached(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := verify(t, s.PublicKeyArmored(), data, sig); err != nil {
		t.Fatal(err)
	}
	if err := verify(t, s.PublicKeyArmored(), []byte("tampered"), sig); err == nil {
		t.Fatal("tampered data verified")
	}
}

func TestKeyPersistsAcrossLoads(t *testing.T) {
	p := filepath.Join(t.TempDir(), "key.asc")
	a, err := LoadOrCreate(p)
	if err != nil {
		t.Fatal(err)
	}
	b, err := LoadOrCreate(p)
	if err != nil {
		t.Fatal(err)
	}
	if a.PublicKeyArmored() != b.PublicKeyArmored() {
		t.Fatal("key changed across loads")
	}
}

func TestKeyFileMode0600(t *testing.T) {
	p := filepath.Join(t.TempDir(), "key.asc")
	if _, err := LoadOrCreate(p); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode().Perm())
	}
}

func TestPublicKeyHasNoPrivateMaterial(t *testing.T) {
	s, _ := LoadOrCreate(filepath.Join(t.TempDir(), "key.asc"))
	if strings.Contains(s.PublicKeyArmored(), "PRIVATE") {
		t.Fatal("private key leaked in public armor")
	}
}
