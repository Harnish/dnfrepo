package adminauth

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const tokenPrefix = "dnfr_"

// Token is an API token's public record; the secret is never stored or returned again.
type Token struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
}

// bearer returns the token from "Authorization: Bearer ...", or "".
func bearer(r *http.Request) string {
	t, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return strings.TrimSpace(t)
}

// CreateToken makes an API token and returns it once.
func (s *Service) CreateToken(ctx context.Context, name, by string) (int64, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 {
		return 0, "", fmt.Errorf("name must be 1-64 characters")
	}
	tok := tokenPrefix + randToken()
	var id int64
	err := s.st.Pool().QueryRow(ctx, `INSERT INTO admin_tokens(name, token_hash, created_by) VALUES($1,$2,$3) RETURNING id`,
		name, hashToken(tok), by).Scan(&id)
	return id, tok, err
}

func (s *Service) ListTokens(ctx context.Context) ([]Token, error) {
	rows, err := s.st.Pool().Query(ctx, `SELECT id, name, created_by, created_at FROM admin_tokens ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Token{}
	for rows.Next() {
		var t Token
		if err := rows.Scan(&t.ID, &t.Name, &t.CreatedBy, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Service) DeleteToken(ctx context.Context, id int64) error {
	_, err := s.st.Pool().Exec(ctx, `DELETE FROM admin_tokens WHERE id=$1`, id)
	return err
}
