-- Cache mode: a mirror that fetches packages from upstream on first request instead of syncing.
ALTER TABLE mirrors ADD COLUMN mode text NOT NULL DEFAULT 'sync' CHECK (mode IN ('sync','cache'));
-- When a package was last served (or stored); cache-mode retention keeps the most recently pulled.
ALTER TABLE packages ADD COLUMN last_pulled timestamptz NOT NULL DEFAULT now();
-- A cache repo's view of upstream's index: every package a client may request, by request path.
CREATE TABLE cache_entries (
  repo_id bigint NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  path text NOT NULL,
  href text NOT NULL,
  filename text NOT NULL,
  name text NOT NULL,
  arch text NOT NULL,
  version text NOT NULL,
  release text NOT NULL,
  sha256 text NOT NULL,
  size bigint NOT NULL,
  component text NOT NULL DEFAULT '',
  control text NOT NULL DEFAULT '',
  md5 text NOT NULL DEFAULT '',
  sha1 text NOT NULL DEFAULT '',
  PRIMARY KEY (repo_id, path)
);
CREATE INDEX cache_entries_file_idx ON cache_entries(repo_id, filename);
