package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type RepoUser struct {
	ID        int64
	Name      string
	Revoked   bool
	CreatedAt time.Time
}

func hashTok(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

// CreateRepoUser makes a repo user and returns its token. The token is not recoverable later.
func (s *Store) CreateRepoUser(ctx context.Context, name string) (int64, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || strings.Contains(name, ":") {
		return 0, "", fmt.Errorf("username must be non-empty and must not contain ':'")
	}
	b := make([]byte, 32)
	rand.Read(b)
	tok := base64.RawURLEncoding.EncodeToString(b)
	var id int64
	err := s.pool.QueryRow(ctx, `INSERT INTO repo_users(name, token_hash) VALUES($1,$2) RETURNING id`, name, hashTok(tok)).Scan(&id)
	if pgCode(err) == "23505" {
		return 0, "", ErrExists
	}
	return id, tok, err
}

func (s *Store) ListRepoUsers(ctx context.Context) ([]RepoUser, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, revoked, created_at FROM repo_users ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RepoUser
	for rows.Next() {
		var u RepoUser
		if err := rows.Scan(&u.ID, &u.Name, &u.Revoked, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) RevokeRepoUser(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `UPDATE repo_users SET revoked=true WHERE id=$1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) GrantRepoUser(ctx context.Context, userID, repoID int64) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO repo_user_grants(user_id, repo_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, userID, repoID)
	return err
}

func (s *Store) UngrantRepoUser(ctx context.Context, userID, repoID int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM repo_user_grants WHERE user_id=$1 AND repo_id=$2`, userID, repoID)
	return err
}

// SetRepoGrants replaces the set of users allowed on a repo.
func (s *Store) SetRepoGrants(ctx context.Context, repoID int64, userIDs []int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM repo_user_grants WHERE repo_id=$1`, repoID); err != nil {
		return err
	}
	for _, u := range userIDs {
		if _, err := tx.Exec(ctx, `INSERT INTO repo_user_grants(user_id, repo_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, u, repoID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) RepoGrants(ctx context.Context, repoID int64) ([]int64, error) {
	rows, err := s.pool.Query(ctx, `SELECT user_id FROM repo_user_grants WHERE repo_id=$1 ORDER BY user_id`, repoID)
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

// CheckRepoToken reports whether user/token is a non-revoked user granted on repoID.
func (s *Store) CheckRepoToken(ctx context.Context, repoID int64, user, token string) (bool, error) {
	var hash string
	var revoked bool
	err := s.pool.QueryRow(ctx, `
		SELECT u.token_hash, u.revoked FROM repo_users u
		JOIN repo_user_grants g ON g.user_id=u.id AND g.repo_id=$1
		WHERE u.name=$2`, repoID, user).Scan(&hash, &revoked)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return !revoked && subtle.ConstantTimeCompare([]byte(hash), []byte(hashTok(token))) == 1, nil
}
