-- Long-lived API tokens for scripts/CI. Only the sha256 is stored; the token is shown once at creation.
CREATE TABLE admin_tokens (
  id bigserial PRIMARY KEY,
  name text NOT NULL UNIQUE,
  token_hash text NOT NULL UNIQUE,
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
