package signer

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/clearsign"
)

func TestClearSignVerifies(t *testing.T) {
	s, err := LoadOrCreate(filepath.Join(t.TempDir(), "k.asc"))
	if err != nil {
		t.Fatal(err)
	}
	msg := "Origin: dnfrepo\nSuite: stable\nSHA256:\n - dashed line\n"
	out, err := s.ClearSign([]byte(msg))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(out), "-----BEGIN PGP SIGNED MESSAGE-----") {
		t.Fatalf("not clearsigned:\n%s", out)
	}
	blk, _ := clearsign.Decode(out)
	if blk == nil {
		t.Fatal("clearsign.Decode failed")
	}
	if strings.TrimRight(strings.ReplaceAll(string(blk.Bytes), "\r\n", "\n"), "\n") != strings.TrimRight(msg, "\n") { // clearsign canonicalizes to CRLF
		t.Fatalf("body changed: %q", blk.Bytes)
	}
	kr, err := openpgp.ReadArmoredKeyRing(strings.NewReader(s.PublicKeyArmored()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := blk.VerifySignature(kr, nil); err != nil {
		t.Fatalf("signature does not verify: %v", err)
	}
	// tampering must fail
	bad, _ := clearsign.Decode([]byte(strings.Replace(string(out), "stable", "evil", 1)))
	if bad != nil {
		if _, err := bad.VerifySignature(kr, nil); err == nil {
			t.Fatal("tampered message verified")
		}
	}
}
