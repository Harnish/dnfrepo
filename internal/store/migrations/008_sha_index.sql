-- Dedup lookups: find other packages with identical content.
CREATE INDEX IF NOT EXISTS packages_sha_idx ON packages(sha256, size);
