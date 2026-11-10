-- A mirror repo's packages come from syncing an upstream. One row per mirror repo.
CREATE TABLE mirrors (
  repo_id bigint PRIMARY KEY REFERENCES repos(id) ON DELETE CASCADE,
  upstream_url text NOT NULL,
  username text NOT NULL DEFAULT '',
  token_enc bytea,
  gpg_key text NOT NULL DEFAULT '',
  require_sig boolean NOT NULL DEFAULT false,
  arches text[] NOT NULL DEFAULT '{}',
  include_pat text[] NOT NULL DEFAULT '{}',
  exclude_pat text[] NOT NULL DEFAULT '{}',
  keep_latest int NOT NULL DEFAULT 0,
  frequency text NOT NULL DEFAULT 'manual',
  time_of_day text NOT NULL DEFAULT '',
  timezone text NOT NULL DEFAULT '',
  weekdays int[] NOT NULL DEFAULT '{}',
  day_of_month int NOT NULL DEFAULT 0,
  paused boolean NOT NULL DEFAULT false,
  state text NOT NULL DEFAULT 'idle',
  last_run timestamptz,
  last_status text NOT NULL DEFAULT '',
  last_error text NOT NULL DEFAULT '',
  next_run timestamptz
);
