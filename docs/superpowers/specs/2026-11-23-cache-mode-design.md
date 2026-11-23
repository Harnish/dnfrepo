# Mirror cache mode (pull-through) — design

## Intent

Save disk and upstream bandwidth: a mirror repo can run in **cache mode**, storing only packages
that hosts actually request. Same upstream config as a sync mirror; packages are fetched on first
request, kept per the normal retention count, and evicted by least-recent pull.

## Goals

- `mirrors.mode` = `sync` (today's behaviour, default) or `cache`.
- Clients see upstream's full package index; any package in it is downloadable.
- First request for an uncached package fetches it from upstream, verifies it, stores it via
  `pipeline`, and serves it.
- Every served package in a cache repo records `last_pulled`.
- Retention: same limit as today (repo keep, per-name overrides, mirror `keep_latest`; smallest
  positive wins, as in `mirror.Plan`), per (name, arch), but ranked by `last_pulled` desc
  instead of version. Purge removes the local file and row only; the package stays fetchable.

## Non-goals

- Include/exclude/arch filters in cache mode (index is upstream's; UI hides them for cache).
- Age-based eviction ("not pulled in N days"). Count only. Add when disk still grows.
- Cache repos as pipeline stages (already true: mirror repos can't be linked, `ErrMirrorRepo`).
- Switching mode on a repo that has packages.

## Anti-goals

- Serving anything not verified against an upstream-signed (when key set) index.
- Rebuilding metadata on each fetch (createrepo_c on every pull would be ruinous).
- Upstream credentials reaching clients.

## Approaches

- **A. Pass upstream index through, re-signed (chosen).** Refresh on the mirror schedule, verify
  against upstream key, serve with our signature. Reuses upstream fetch, sig checks, scheduler,
  signer. Cost: no filters.
- B. Generate our own rpm index from upstream package list. Filters work, but needs a pure-Go
  repodata writer (createrepo_c needs local files). Rejected: big, fragile.
- C. Live-proxy index per request with TTL. Always fresh, but an upstream outage hides even cached
  packages and signs per request. Rejected.

User picked A.

## Design

### Schema (`011_cache.sql`)

- `ALTER TABLE mirrors ADD COLUMN mode text NOT NULL DEFAULT 'sync' CHECK (mode IN ('sync','cache'))`.
- `ALTER TABLE packages ADD COLUMN last_pulled timestamptz` (set on ingest = now, and on each serve).
- `CREATE TABLE cache_entries (repo_id bigint REFERENCES repos ON DELETE CASCADE, path text,
  filename text, name, arch, version, release, sha256 text, size bigint, href text,
  component text, control text, md5 text, sha1 text, PRIMARY KEY (repo_id, path))` plus index on
  `(repo_id, filename)`. Replaced wholesale per refresh in one transaction (COPY). `path` is what
  the client requests (rpm: upstream `location href`; deb: `pool/<component>/<filename>`);
  `href` is the upstream download path.

### Index refresh (`mirror`, mode = cache)

`Syncer.run` branches on mode. Cache refresh, under the existing state/batch bookkeeping minus the
batch (no batch rows for cache):

- **rpm:** fetch + verify `repomd.xml` (and `.asc` when key set) as `Upstream.Packages` does, then
  download every `<data>` file it lists, verifying each sha256 from repomd; only `repodata/<name>`
  locations accepted, else refresh fails. Parse primary into entries. Write files into
  `repodata.<gen>/`, add our `repomd.xml.asc` (signer), atomic symlink swap, prune generations —
  same as `publish.Rebuild`. New `publish.SwapRepodata(slug, files map[string][]byte)` (or a
  dir) holding the publisher lock, so the swap code isn't duplicated.
- **deb:** `AptUpstream.Packages` already verifies Release and indexes and returns stanzas.
  Generate our archive with `aptindex.Build` from the entries (MD5/SHA1 from the upstream stanza)
  and swap via the `rebuildApt` path refactored to take `[]aptindex.Package`.
- Entries and index swap are committed together: write `cache_entries`, then swap. A crash between
  leaves entries newer than the index — harmless (a client can only request what the old index
  lists; if that path is gone from entries it 404s until next refresh).
- After refresh, purge cached packages whose filename is no longer in `cache_entries` (unreachable
  by clients), audit `purged`. This is the one purge not bounded by the keep limit.
- Status strings: `refreshed` / error. "Sync now" in UI reads "Refresh index" for cache mirrors.

### Serving (`serve`)

- Cache repo request for a package path:
  1. Local row + regular file exists → serve as today, then `TouchPackage(repo, filename)`.
  2. Else look up `cache_entries` by path. Missing → 404 (no upstream call).
  3. Else fetch via a new `mirror.Fetcher` (injected into `serve.Deps`; nil when not wired).
- rpm path rule widens for cache repos only: any `/`-separated path of `repodataRe`-safe segments
  ending `.rpm`, matched exactly against `cache_entries.path`. Files are stored flat by filename
  (as pipeline does). Deb paths unchanged (`pool/<component>/<file>.deb`).
- `last_pulled` write on every package GET of a cache repo (one indexed UPDATE). Range/resume
  requests touch again; fine.

### Fetch (`mirror.Fetcher`)

- Per (repo, filename) in-flight map. First requester: download from upstream with server-side
  creds (`downloadVerified` path), tee to the client and a staging file under
  `<DATA_DIR>/mirror-staging/<repo>/`, hashing. The download runs on
  `context.WithoutCancel` + `Syncer.Timeout`-style cap so a client disconnect still completes the
  cache fill. Client write errors are ignored after disconnect.
- Concurrent requesters for the same in-flight file: proxied straight from upstream (no store).
- On completion: size + sha256 must match the entry; rpm header name/arch/version checked like
  `checkHeaders`. Mismatch → discard staging file, log; that client already got bytes, and dnf/apt
  reject them on checksum.
- Store via new `pipeline.IngestCached(ctx, slug, StagedFile, actor="cache")`: under the repo
  lock, link file, insert row with `last_pulled = now()`, run purge, **no metadata rebuild**.
  Audit `cached`.
- Upstream unreachable and not cached → 502.
- Response headers: Content-Length from entry size; Content-Type as `ServeContent` would.

### Retention (`pipeline`)

`purgeLocked` gets the cache ordering: for cache repos rank each (name, arch) group by
`last_pulled` desc (NULL last, then version desc as tiebreak) instead of version. Limit
computation includes mirror `keep_latest` for cache repos. Removes file + row, audit `purged`,
no rebuild for cache repos.

### API / UI

- `mirrorJSON.mode`; `PUT` accepts it; changing mode on a repo with packages → 409.
- Mirror form: mode toggle; in cache mode hide arches/include/exclude, relabel sync button.
- Package list shows `last_pulled` for cache repos and sorts by it desc.
- README + Help page: cache mode section.

## Invariants kept

- Package files written only by `pipeline` (`IngestCached`, purge), under the per-repo mutex.
- Metadata swap atomic via generation dir + symlink.
- Access mode guard unchanged; uncached fetches only for paths in a verified index, so anonymous
  public users can't make us fetch arbitrary upstream URLs.
- Upstream token sealed, used server-side only.

## Left to the builder

- Exact `cache_entries` COPY mechanics and refresh transaction shape.
- Whether rpm `<data>` files are buffered in memory or streamed to disk (filelists can be ~50 MB;
  prefer disk).
- Fetcher wiring in `main`, test fakes (extend `fakeupstream_test.go` / `fakeapt_test.go`).

## Known ceilings

- Metadata swap vs. in-flight cache fill: a fill for a path dropped by the new index still stores;
  the post-refresh orphan purge catches it next refresh.
- Whole upstream index stored per cache repo (DB + disk) even though few packages are cached.
- First pull of a big package is at upstream speed.
