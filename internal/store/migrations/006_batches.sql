-- One row per mirror sync run; packages.origin_batch_id records which run brought a package in
-- and is copied on promotion, so a batch follows its packages through the stages.
CREATE TABLE sync_batches (
  id bigserial PRIMARY KEY,
  repo_id bigint NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  started_at timestamptz NOT NULL DEFAULT now(),
  finished_at timestamptz,
  status text NOT NULL DEFAULT 'running',
  added_count int NOT NULL DEFAULT 0,
  error text NOT NULL DEFAULT ''
);
CREATE INDEX sync_batches_repo_idx ON sync_batches(repo_id, id DESC);
ALTER TABLE packages ADD COLUMN origin_batch_id bigint REFERENCES sync_batches(id) ON DELETE SET NULL;
