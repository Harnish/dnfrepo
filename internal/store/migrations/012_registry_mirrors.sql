-- Registry image mirrors: follow an upstream image tag and keep the last N dated copies.
CREATE TABLE registry_mirrors (
  id bigserial PRIMARY KEY,
  upstream text NOT NULL,                       -- normalized, e.g. docker.io/plexinc/pms-docker
  tag text NOT NULL,
  local_name text NOT NULL,
  platforms text[] NOT NULL DEFAULT '{}',       -- os/arch[/variant]; empty = all
  keep int NOT NULL DEFAULT 3 CHECK (keep >= 1),
  username text NOT NULL DEFAULT '',
  secret bytea,                                 -- token sealed with SECRET_KEY
  upstream_digest text NOT NULL DEFAULT '',
  last_run timestamptz,
  last_status text NOT NULL DEFAULT '',
  last_error text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (local_name, tag)
);
-- Retention ledger: only tags listed here are ever pruned.
CREATE TABLE registry_mirror_versions (
  mirror_id bigint NOT NULL REFERENCES registry_mirrors(id) ON DELETE CASCADE,
  tag text NOT NULL,
  digest text NOT NULL,
  upstream_digest text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  PRIMARY KEY (mirror_id, tag)
);
