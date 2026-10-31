-- Admins now come from the OIDC provider (group membership checked at login), so the
-- local account table goes away. Sessions are keyed by email; existing sessions are dropped.
DROP TABLE admin_sessions;
DROP TABLE admins;
CREATE TABLE admin_sessions (
  token_hash text PRIMARY KEY,
  email text NOT NULL,
  expires_at timestamptz NOT NULL
);
