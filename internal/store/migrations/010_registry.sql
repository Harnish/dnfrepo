-- Container registry: per-image settings, IP allow-lists, user grants, push tokens.
-- Images with no row use the defaults (public, not listed).
CREATE TABLE registry_repos (
  name text PRIMARY KEY,
  listed boolean NOT NULL DEFAULT false,
  mode text NOT NULL DEFAULT 'public' CHECK (mode IN ('public','ips','users')),
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE registry_ips (
  name text NOT NULL REFERENCES registry_repos(name) ON DELETE CASCADE,
  prefix cidr NOT NULL,
  PRIMARY KEY (name, prefix)
);
CREATE TABLE registry_grants (
  user_id bigint NOT NULL REFERENCES repo_users(id) ON DELETE CASCADE,
  name text NOT NULL REFERENCES registry_repos(name) ON DELETE CASCADE,
  PRIMARY KEY (user_id, name)
);
CREATE TABLE registry_push_tokens (
  id bigserial PRIMARY KEY,
  name text NOT NULL UNIQUE CHECK (name NOT LIKE '%:%'),
  token_hash text NOT NULL,
  revoked boolean NOT NULL DEFAULT false,
  created_by text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now()
);
