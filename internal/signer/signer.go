// Package signer holds the instance GPG key used to sign repo metadata.
package signer

import (
	"bytes"
	"os"
	"path/filepath"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/clearsign"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

type Signer struct {
	entity *openpgp.Entity
	pub    string
}

// LoadOrCreate reads the armored private key at path, generating it (0600) if absent.
func LoadOrCreate(path string) (*Signer, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		e, err := openpgp.NewEntity("dnfrepo", "repo metadata signing key", "dnfrepo@localhost", &packet.Config{RSABits: 3072})
		if err != nil {
			return nil, err
		}
		var buf bytes.Buffer
		w, err := armor.Encode(&buf, openpgp.PrivateKeyType, nil)
		if err != nil {
			return nil, err
		}
		if err := e.SerializePrivate(w, nil); err != nil {
			return nil, err
		}
		w.Close()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
			return nil, err
		}
		b = buf.Bytes()
	} else if err != nil {
		return nil, err
	}
	kr, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	e := kr[0]
	var pub bytes.Buffer
	w, err := armor.Encode(&pub, openpgp.PublicKeyType, nil)
	if err != nil {
		return nil, err
	}
	if err := e.Serialize(w); err != nil {
		return nil, err
	}
	w.Close()
	return &Signer{entity: e, pub: pub.String()}, nil
}

// SignDetached returns an ASCII-armored detached signature of data.
func (s *Signer) SignDetached(data []byte) ([]byte, error) {
	var out bytes.Buffer
	if err := openpgp.ArmoredDetachSign(&out, s.entity, bytes.NewReader(data), nil); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// ClearSign returns data as a cleartext-signed message (apt's InRelease form).
func (s *Signer) ClearSign(data []byte) ([]byte, error) {
	var out bytes.Buffer
	w, err := clearsign.Encode(&out, s.entity.PrivateKey, nil)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func (s *Signer) PublicKeyArmored() string { return s.pub }
