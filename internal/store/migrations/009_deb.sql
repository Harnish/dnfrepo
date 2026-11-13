-- apt/deb repos. kind is fixed at creation; deb_repos holds the suite, components and
-- architectures of a deb repo (for a mirror, those of the upstream suite).
ALTER TABLE repos ADD COLUMN kind text NOT NULL DEFAULT 'rpm' CHECK (kind IN ('rpm','deb'));
CREATE TABLE deb_repos (
  repo_id bigint PRIMARY KEY REFERENCES repos(id) ON DELETE CASCADE,
  suite text NOT NULL,
  components text[] NOT NULL,
  arches text[] NOT NULL
);
-- Deb index data; '' for rpm rows. control is the verbatim control stanza.
ALTER TABLE packages ADD COLUMN component text NOT NULL DEFAULT '';
ALTER TABLE packages ADD COLUMN control text NOT NULL DEFAULT '';
ALTER TABLE packages ADD COLUMN md5 text NOT NULL DEFAULT '';
ALTER TABLE packages ADD COLUMN sha1 text NOT NULL DEFAULT '';
