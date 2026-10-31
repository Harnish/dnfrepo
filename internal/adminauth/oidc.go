package adminauth

import (
	"context"
	"crypto/subtle"
	"errors"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"dnfrepo/internal/config"
)

// LazyIDP discovers the provider on first use and retries after a failure, so dnfrepo starts
// and keeps serving repos while Authentik is down; only admin login waits for it.
func LazyIDP(cfg config.Config) IDP {
	var mu sync.Mutex
	var real *IDP
	get := func(ctx context.Context) (IDP, error) {
		mu.Lock()
		defer mu.Unlock()
		if real != nil {
			return *real, nil
		}
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second) // go-oidc detaches cancellation for later key fetches
		defer cancel()
		i, err := DiscoverIDP(ctx, cfg)
		if err != nil {
			return IDP{}, err
		}
		real = &i
		return i, nil
	}
	return IDP{
		AuthURL: func(state, nonce, challenge string) (string, error) {
			i, err := get(context.Background())
			if err != nil {
				return "", err
			}
			return i.AuthURL(state, nonce, challenge)
		},
		Exchange: func(ctx context.Context, code, verifier, nonce string) (Claims, error) {
			i, err := get(ctx)
			if err != nil {
				return Claims{}, err
			}
			return i.Exchange(ctx, code, verifier, nonce)
		},
	}
}

// DiscoverIDP contacts the issuer's discovery endpoint and returns the real provider.
func DiscoverIDP(ctx context.Context, cfg config.Config) (IDP, error) {
	p, err := oidc.NewProvider(ctx, cfg.OIDCIssuer)
	if err != nil {
		return IDP{}, err
	}
	oc := oauth2.Config{
		ClientID:     cfg.OIDCClientID,
		ClientSecret: cfg.OIDCClientSecret,
		Endpoint:     p.Endpoint(),
		RedirectURL:  cfg.BaseURL + "/api/auth/oidc/callback",
		Scopes:       []string{oidc.ScopeOpenID, "email", "profile"},
	}
	verifier := p.Verifier(&oidc.Config{ClientID: cfg.OIDCClientID}) // checks signature, issuer, audience, expiry
	return IDP{
		AuthURL: func(state, nonce, challenge string) (string, error) {
			return oc.AuthCodeURL(state, oidc.Nonce(nonce),
				oauth2.SetAuthURLParam("code_challenge", challenge),
				oauth2.SetAuthURLParam("code_challenge_method", "S256")), nil
		},
		Exchange: func(ctx context.Context, code, pkceVerifier, nonce string) (Claims, error) {
			tok, err := oc.Exchange(ctx, code, oauth2.VerifierOption(pkceVerifier))
			if err != nil {
				return Claims{}, err
			}
			raw, _ := tok.Extra("id_token").(string)
			if raw == "" {
				return Claims{}, errors.New("no id_token in token response")
			}
			idt, err := verifier.Verify(ctx, raw)
			if err != nil {
				return Claims{}, err
			}
			if subtle.ConstantTimeCompare([]byte(idt.Nonce), []byte(nonce)) != 1 { // go-oidc does not check the nonce
				return Claims{}, errors.New("id_token nonce mismatch")
			}
			var c struct {
				Email  string   `json:"email"`
				Groups []string `json:"groups"`
			}
			if err := idt.Claims(&c); err != nil {
				return Claims{}, err
			}
			return Claims{Email: c.Email, Groups: c.Groups}, nil
		},
	}, nil
}
