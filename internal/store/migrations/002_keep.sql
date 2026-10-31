ALTER TABLE repos ADD COLUMN keep integer NOT NULL DEFAULT 0 CHECK (keep >= 0);
CREATE TABLE repo_keep_overrides (
  repo_id bigint NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  package text NOT NULL,
  keep integer NOT NULL CHECK (keep >= 0),
  PRIMARY KEY (repo_id, package)
);
