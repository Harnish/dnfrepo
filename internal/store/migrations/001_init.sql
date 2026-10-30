CREATE TABLE admins (
  id bigserial PRIMARY KEY,
  email text NOT NULL UNIQUE,
  password_hash text,
  verified boolean NOT NULL DEFAULT false,
  verify_token_hash text,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE admin_sessions (
  token_hash text PRIMARY KEY,
  admin_id bigint NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
  expires_at timestamptz NOT NULL
);
CREATE TABLE repos (
  id bigserial PRIMARY KEY,
  slug text NOT NULL UNIQUE,
  name text NOT NULL,
  mode text NOT NULL CHECK (mode IN ('public','ips','users')),
  previous_id bigint UNIQUE REFERENCES repos(id),
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE repo_ips (
  repo_id bigint NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  prefix cidr NOT NULL,
  PRIMARY KEY (repo_id, prefix)
);
CREATE TABLE repo_users (
  id bigserial PRIMARY KEY,
  name text NOT NULL UNIQUE CHECK (name NOT LIKE '%:%'),
  token_hash text NOT NULL,
  revoked boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE repo_user_grants (
  user_id bigint NOT NULL REFERENCES repo_users(id) ON DELETE CASCADE,
  repo_id bigint NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  PRIMARY KEY (user_id, repo_id)
);
CREATE TABLE packages (
  id bigserial PRIMARY KEY,
  repo_id bigint NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  filename text NOT NULL,
  name text NOT NULL,
  version text NOT NULL,
  release text NOT NULL,
  arch text NOT NULL,
  sha256 text NOT NULL,
  size bigint NOT NULL,
  added_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (repo_id, filename)
);
CREATE TABLE sources (
  id bigserial PRIMARY KEY,
  repo_id bigint NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  kind text NOT NULL CHECK (kind IN ('github','plex')),
  spec text NOT NULL DEFAULT '',
  last_run timestamptz,
  last_status text NOT NULL DEFAULT '',
  last_error text NOT NULL DEFAULT ''
);
CREATE TABLE audit_events (
  id bigserial PRIMARY KEY,
  repo_id bigint REFERENCES repos(id) ON DELETE CASCADE,
  actor text NOT NULL,
  action text NOT NULL,
  detail text NOT NULL DEFAULT '',
  at timestamptz NOT NULL DEFAULT now()
);
