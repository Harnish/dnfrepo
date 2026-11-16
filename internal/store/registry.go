package store

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var ErrBadMode = errors.New("mode must be public, ips or users")

// RegistryRepo is the settings of one container image repository. A name with no row has
// the defaults (public, not listed) and Exists=false.
type RegistryRepo struct {
	Name   string
	Listed bool
	Mode   AccessMode
	Exists bool
}

func (s *Store) RegistryRepoByName(ctx context.Context, name string) (RegistryRepo, error) {
	r := RegistryRepo{Name: name, Mode: ModePublic}
	err := s.pool.QueryRow(ctx, `SELECT listed, mode FROM registry_repos WHERE name=$1`, name).Scan(&r.Listed, &r.Mode)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, nil
	}
	r.Exists = err == nil
	return r, err
}

// EnsureRegistryRepo inserts the default settings row if the name has none.
func (s *Store) EnsureRegistryRepo(ctx context.Context, name string) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO registry_repos(name) VALUES($1) ON CONFLICT DO NOTHING`, name)
	return err
}

func (s *Store) ListRegistryRepos(ctx context.Context) ([]RegistryRepo, error) {
	rows, err := s.pool.Query(ctx, `SELECT name, listed, mode FROM registry_repos ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RegistryRepo
	for rows.Next() {
		r := RegistryRepo{Exists: true}
		if err := rows.Scan(&r.Name, &r.Listed, &r.Mode); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) SetRegistryRepo(ctx context.Context, name string, listed bool, mode AccessMode) error {
	if !mode.Valid() {
		return ErrBadMode
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO registry_repos(name, listed, mode) VALUES($1,$2,$3)
		ON CONFLICT (name) DO UPDATE SET listed=$2, mode=$3`, name, listed, string(mode))
	return err
}

func (s *Store) DeleteRegistryRepo(ctx context.Context, name string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM registry_repos WHERE name=$1`, name)
	return err
}

func (s *Store) SetRegistryIPs(ctx context.Context, name string, ps []netip.Prefix) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `INSERT INTO registry_repos(name) VALUES($1) ON CONFLICT DO NOTHING`, name); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM registry_ips WHERE name=$1`, name); err != nil {
		return err
	}
	for _, p := range ps {
		if _, err := tx.Exec(ctx, `INSERT INTO registry_ips(name,prefix) VALUES($1,$2) ON CONFLICT DO NOTHING`, name, p.Masked().String()); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) RegistryIPs(ctx context.Context, name string) ([]netip.Prefix, error) {
	rows, err := s.pool.Query(ctx, `SELECT prefix::text FROM registry_ips WHERE name=$1 ORDER BY prefix`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []netip.Prefix
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		p, err := netip.ParsePrefix(t)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) SetRegistryGrants(ctx context.Context, name string, userIDs []int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `INSERT INTO registry_repos(name) VALUES($1) ON CONFLICT DO NOTHING`, name); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM registry_grants WHERE name=$1`, name); err != nil {
		return err
	}
	for _, u := range userIDs {
		if _, err := tx.Exec(ctx, `INSERT INTO registry_grants(user_id, name) VALUES($1,$2) ON CONFLICT DO NOTHING`, u, name); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) RegistryGrants(ctx context.Context, name string) ([]int64, error) {
	rows, err := s.pool.Query(ctx, `SELECT user_id FROM registry_grants WHERE name=$1 ORDER BY user_id`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// CheckRegistryUser reports whether user/token is a non-revoked repo user granted on image name.
func (s *Store) CheckRegistryUser(ctx context.Context, name, user, token string) (bool, error) {
	var hash string
	var revoked bool
	err := s.pool.QueryRow(ctx, `
		SELECT u.token_hash, u.revoked FROM repo_users u
		JOIN registry_grants g ON g.user_id=u.id AND g.name=$1
		WHERE u.name=$2`, name, user).Scan(&hash, &revoked)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return !revoked && subtle.ConstantTimeCompare([]byte(hash), []byte(hashTok(token))) == 1, nil
}

// PushToken is a global credential that may push (and pull) any registry image.
type PushToken struct {
	ID        int64
	Name      string
	Revoked   bool
	CreatedBy string
	CreatedAt time.Time
}

// CreatePushToken returns the new token's id and secret; the secret is not recoverable later.
func (s *Store) CreatePushToken(ctx context.Context, name, createdBy string) (int64, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || strings.Contains(name, ":") {
		return 0, "", fmt.Errorf("name must be non-empty and must not contain ':'")
	}
	b := make([]byte, 32)
	rand.Read(b)
	tok := base64.RawURLEncoding.EncodeToString(b)
	var id int64
	err := s.pool.QueryRow(ctx, `INSERT INTO registry_push_tokens(name, token_hash, created_by) VALUES($1,$2,$3) RETURNING id`,
		name, hashTok(tok), createdBy).Scan(&id)
	if pgCode(err) == "23505" {
		return 0, "", ErrExists
	}
	return id, tok, err
}

func (s *Store) ListPushTokens(ctx context.Context) ([]PushToken, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, revoked, created_by, created_at FROM registry_push_tokens ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PushToken
	for rows.Next() {
		var t PushToken
		if err := rows.Scan(&t.ID, &t.Name, &t.Revoked, &t.CreatedBy, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) RevokePushToken(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `UPDATE registry_push_tokens SET revoked=true WHERE id=$1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) CheckPushToken(ctx context.Context, user, token string) (bool, error) {
	var hash string
	var revoked bool
	err := s.pool.QueryRow(ctx, `SELECT token_hash, revoked FROM registry_push_tokens WHERE name=$1`, user).Scan(&hash, &revoked)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return !revoked && subtle.ConstantTimeCompare([]byte(hash), []byte(hashTok(token))) == 1, nil
}
