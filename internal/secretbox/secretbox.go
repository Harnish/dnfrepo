// Package secretbox seals small secrets (upstream credentials) with AES-GCM.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
)

type Box struct{ gcm cipher.AEAD }

// New derives an AES-256 key as sha256(key).
func New(key string) (*Box, error) {
	if key == "" {
		return nil, errors.New("secretbox: empty key")
	}
	k := sha256.Sum256([]byte(key))
	blk, err := aes.NewCipher(k[:])
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	return &Box{g}, nil
}

// Seal returns nonce||ciphertext.
func (b *Box) Seal(plain string) ([]byte, error) {
	n := make([]byte, b.gcm.NonceSize())
	if _, err := rand.Read(n); err != nil {
		return nil, err
	}
	return b.gcm.Seal(n, n, []byte(plain), nil), nil
}

func (b *Box) Open(sealed []byte) (string, error) {
	ns := b.gcm.NonceSize()
	if len(sealed) < ns {
		return "", errors.New("secretbox: sealed value too short")
	}
	p, err := b.gcm.Open(nil, sealed[:ns], sealed[ns:], nil)
	if err != nil {
		return "", errors.New("secretbox: cannot open")
	}
	return string(p), nil
}
