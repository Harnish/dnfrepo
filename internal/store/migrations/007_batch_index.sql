-- Batch lookups by origin (ListBatches, BatchFilenames, ON DELETE SET NULL when a batch row goes).
CREATE INDEX IF NOT EXISTS packages_origin_batch_idx ON packages(origin_batch_id) WHERE origin_batch_id IS NOT NULL;
