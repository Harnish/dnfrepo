# Mirror Cache Mode Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A mirror can run in `cache` mode: publish upstream's index re-signed, fetch packages from upstream on first request, keep them per the normal limit ranked by last pull.

**Architecture:** `mirrors.mode` selects sync vs cache. Cache refresh (scheduler/"sync now") downloads + verifies upstream's index, stores the package list in `cache_entries`, and swaps a signed index in through `pipeline.RefreshCache`. `serve` looks requested package paths up in `cache_entries`; a miss streams from upstream through `mirror.Syncer.Fetch`, which stages, verifies and ingests via `pipeline.IngestBatch` (batch 0). `publish.Publisher.Rebuild` is a no-op for cache repos so no caller ever replaces upstream's index with a local-only one.

**Tech Stack:** Go, pgx/Postgres, createrepo_c (not used for cache repos), React/TS admin UI.

**Spec:** `docs/superpowers/specs/2026-11-23-cache-mode-design.md`

## Global Constraints

- Mode values exactly `sync` (default) and `cache`.
- All package file writes go through `pipeline` under the per-repo lock (`s.lock(slug)`).
- Index swap atomic: `repodata.<gen>` / `dists.<gen>` + symlink rename, previous generation kept (`pruneGenerations`).
- Upstream token never in logs/responses; errors through `scrub`.
- Serve only paths present in `cache_entries` of a verified index; no other upstream request from `serve`.
- DB tests: `DNFREPO_TEST_DSN=postgres://postgres:pw@localhost:5433/postgres go test ./...` (skip silently without it — always set it).
- TDD, conventional commits, ask the user before each commit (project rule) — batch-confirm once at start of execution.
- New migration file `internal/store/migrations/011_cache.sql`; never edit applied ones.

## Review Focus

1. Any `Rebuild` on a cache repo (admin "rebuild", package delete, ApplyKeep, QuarantineOrphans, deb settings `OnChange`) must not overwrite the upstream index — test in Task 2.
2. Client disconnects mid-first-download: fill still completes and stores — test in Task 5.
3. Upstream corrupt body (sha mismatch) on first fetch: nothing stored, file absent, row absent — test in Task 5.
4. Two concurrent requests for the same uncached package: one store, both clients get full bytes — test in Task 5.
5. Upstream down, package already cached: still served; not cached: 502, no partial headers sent — test in Task 6.

---

### Task 1: Store — mode, last_pulled, cache_entries

**Files:**
- Create: `internal/store/migrations/011_cache.sql`, `internal/store/cache.go`, `internal/store/cache_test.go`
- Modify: `internal/store/mirrors.go` (Mode in struct/cols/scan/SetMirror), `internal/store/packages.go` (`LastPulled` in `Package`, `pkgCols`, `scanPkg`), `internal/store/batches.go:81` (`AddMirrorPackages`)

**Interfaces:**
- Produces:
  - `store.ModeSync = "sync"`, `store.ModeCache = "cache"`; `Mirror.Mode string`
  - `store.ErrModeChange = errors.New("mirror mode can only change while the repo has no packages")`
  - `Package.LastPulled time.Time`
  - `type CacheEntry struct { Path, Href, Filename, Name, Arch, Version, Release, SHA256 string; Size int64; Component, Control, MD5, SHA1 string }`
  - `(*Store) ReplaceCacheEntries(ctx, repoID int64, es []CacheEntry) error` — one tx: DELETE then `CopyFrom`
  - `(*Store) CacheEntryByPath(ctx, repoID int64, path string) (CacheEntry, error)` — `ErrNotFound`
  - `(*Store) ListCacheEntries(ctx, repoID int64) ([]CacheEntry, error)`
  - `(*Store) UncachedPackages(ctx, repoID int64) ([]string, error)` — filenames of package rows with no `cache_entries` row of that filename
  - `(*Store) TouchPackage(ctx, repoID int64, filename string) error`
  - `(*Store) IsCacheRepo(ctx, repoID int64) (bool, error)`
  - `AddMirrorPackages` with `batchID == 0` stores `origin_batch_id` NULL and audits `cached` instead of `synced`

- [ ] **Step 1: Migration** `011_cache.sql`:
  `ALTER TABLE mirrors ADD COLUMN mode text NOT NULL DEFAULT 'sync' CHECK (mode IN ('sync','cache'));`
  `ALTER TABLE packages ADD COLUMN last_pulled timestamptz NOT NULL DEFAULT now();`
  `CREATE TABLE cache_entries (repo_id bigint NOT NULL REFERENCES repos(id) ON DELETE CASCADE, path text NOT NULL, href text NOT NULL, filename text NOT NULL, name text NOT NULL, arch text NOT NULL, version text NOT NULL, release text NOT NULL, sha256 text NOT NULL, size bigint NOT NULL, component text NOT NULL DEFAULT '', control text NOT NULL DEFAULT '', md5 text NOT NULL DEFAULT '', sha1 text NOT NULL DEFAULT '', PRIMARY KEY (repo_id, path));`
  `CREATE INDEX cache_entries_file_idx ON cache_entries(repo_id, filename);`

- [ ] **Step 2: Failing tests** in `cache_test.go`:
  - `TestMirrorModeRoundTrip`: SetMirror with Mode cache → MirrorByRepo `.Mode == "cache"`; Mode "" on create → `"sync"`.
  - `TestMirrorModeChangeRefusedWithPackages`: cache mirror, AddMirrorPackages(batch 0, one pkg), SetMirror Mode sync → `errors.Is(err, ErrModeChange)`; same change with no packages → nil.
  - `TestReplaceCacheEntries`: replace with {a,b}, then {b,c}; ListCacheEntries paths == [b c]; CacheEntryByPath("a") → ErrNotFound; all fields of c round-trip.
  - `TestUncachedPackages`: rows x.rpm, y.rpm; entries only y.rpm → `[]string{"x.rpm"}`.
  - `TestTouchPackage`: AddMirrorPackages, read LastPulled t0, Touch, re-read → after t0.
  - `TestAddMirrorPackagesBatchZero`: origin_batch_id nil; ListAudit action `cached`.
  - `TestIsCacheRepo`: sync mirror false, cache mirror true, plain repo false.

- [ ] **Step 3: Run** `DNFREPO_TEST_DSN=... go test ./internal/store/ -run 'Mode|Cache|Uncached|Touch|BatchZero'` → FAIL (undefined).
- [ ] **Step 4: Implement.** SetMirror: default empty Mode to `sync`; when row exists and mode differs and packages count > 0 → `ErrModeChange` (inside the existing FOR UPDATE tx). Add `mode` to INSERT/UPSERT.
- [ ] **Step 5: Run** whole `go test ./internal/store/` → PASS.
- [ ] **Step 6: Commit** `feat(store): mirror cache mode schema and cache entries`.

### Task 2: Publisher — cache index swap, Rebuild no-op for cache

**Files:**
- Modify: `internal/publish/publish.go`, `internal/publish/apt.go`
- Test: `internal/publish/publish_test.go` (or existing test file of the package)

**Interfaces:**
- Consumes: `store.IsCacheRepo`.
- Produces:
  - `(*Publisher) SwapRepodata(slug, src string) error` — `src` holds upstream `repomd.xml` + data files; signs `repomd.xml` into `repomd.xml.asc`, renames src to `repodata.<gen>`, symlink swap, prune. `src` must be on the same filesystem (callers stage under `<DATA_DIR>`). Holds the publisher slug lock.
  - `(*Publisher) PublishApt(ctx, repo store.Repo, pkgs []aptindex.Package) error` — the generation/sign/swap body of today's `rebuildApt`, which now calls it after mapping rows. Holds the lock (callers outside Rebuild).
  - `Rebuild` returns nil without touching disk when `p.Store != nil` and `IsCacheRepo` is true.
  - Refactor the shared tail (sign repomd, link, prune) of `Rebuild` into a helper used by both; no duplication.

- [ ] **Step 1: Failing tests:**
  - `TestRebuildSkipsCacheRepo`: DB cache mirror repo with a sentinel `repodata/` symlink target; Rebuild → nil, symlink target unchanged.
  - `TestSwapRepodataSignsAndSwaps`: src dir with `repomd.xml` + `x-primary.xml.gz`; after swap `repodata/repomd.xml` bytes equal src's, `repomd.xml.asc` verifies with signer public key, a second swap keeps exactly two `repodata.*` gens.
  - `TestPublishAptFromPackages`: one `aptindex.Package` → `dists/<suite>/InRelease` exists and Packages contains `Filename: pool/main/<file>`.
- [ ] **Step 2: Run** → FAIL. **Step 3: Implement.** **Step 4: Run** `go test ./internal/publish/` → PASS.
- [ ] **Step 5: Commit** `feat(publish): swap upstream repodata for cache repos, skip rebuild`.

### Task 3: Pipeline — cache retention and RefreshCache

**Files:**
- Modify: `internal/pipeline/pipeline.go` (Publisher interface), `internal/pipeline/keep.go`, `internal/pipeline/pipeline_test.go` (`fakePub` gains the two methods)
- Create: `internal/pipeline/cache.go`, `internal/pipeline/cache_test.go`

**Interfaces:**
- Consumes: Task 1 store funcs; Task 2 `SwapRepodata`, `PublishApt`.
- Produces:
  - `Publisher` interface adds `SwapRepodata(slug, src string) error` and `PublishApt(ctx context.Context, repo store.Repo, pkgs []aptindex.Package) error`.
  - `(*Service) RefreshCache(ctx, slug string, entries []store.CacheEntry, repodataDir, actor string) ([]string, error)` — under `s.lock(slug)`: `ReplaceCacheEntries`; rpm → `SwapRepodata(slug, repodataDir)`; deb → `PublishApt` with entries mapped to `aptindex.Package{Component, Arch, Filename, Size, MD5, SHA1, SHA256, Control}`; then for each `UncachedPackages` name delete row + file, audit `purged`. Returns purged filenames. Swap runs on `context.WithoutCancel` + `RebuildTimeout`, like `rebuild`.
  - `purgeLocked` for cache repos: limit = smallest positive of repo keep/override and mirror `KeepLatest` (same rule as `mirror.Plan`); group order `LastPulled` desc, then version desc, then filename. Sync and plain repos unchanged.

- [ ] **Step 1: Failing tests** in `cache_test.go`:
  - `TestCachePurgeByLastPulled`: cache mirror, keep 2; ingest a-1, a-2, a-3 (batch 0); Touch a-1; ingest nothing else; ApplyKeep → remaining files {a-1, a-3} (a-2 least recently pulled), audit `purged` a-2.
  - `TestCachePurgeUsesMirrorKeepLatest`: repo keep 0, mirror KeepLatest 1, two versions → one left (the more recently pulled, even if older version).
  - `TestRefreshCachePurgesDroppedFromUpstream`: cached x.rpm, y.rpm; RefreshCache with entries only for y → returns `["x.rpm"]`, x file gone, fakePub recorded one SwapRepodata.
  - `TestSyncMirrorPurgeUnchanged`: existing keep tests still pass (run them).
- [ ] **Step 2: Run** → FAIL. **Step 3: Implement.** **Step 4: Run** `go test ./internal/pipeline/` → PASS.
- [ ] **Step 5: Commit** `feat(pipeline): cache retention by last pull and index refresh`.

### Task 4: Mirror — cache index refresh

**Files:**
- Modify: `internal/mirror/upstream.go` (split `primary` into fetch + `parsePrimary(r io.Reader) ([]UpstreamPkg, error)`), `internal/mirror/sync.go` (`run` branches on mode; extract upstream construction)
- Create: `internal/mirror/cache.go`, `internal/mirror/cache_test.go`

**Interfaces:**
- Consumes: `pipeline.RefreshCache`.
- Produces:
  - `(*Upstream) FetchRepodata(ctx, dir string) ([]UpstreamPkg, error)` — fetch + verify `repomd.xml` (and `.asc` when GPGKey set; RequireSig rule as `Packages`), download every `<data>` location into `dir/<base name>` verifying its sha256 (sha256 only; other type → error) and capped like primary; only `repodata/<repodataRe name>` locations allowed; writes `dir/repomd.xml`; parses primary from the written file.
  - `(*Syncer) source(ctx, m store.Mirror, repo store.Repo, token string) (packageSource, Filter, error)` — today's construction in `run`, reused by refresh and Fetch.
  - `cacheEntries(kind string, pkgs []UpstreamPkg) ([]store.CacheEntry, error)` — rpm: `Path = Href`; deb: `Path = "pool/"+Component+"/"+Filename`, MD5/SHA1 from `MD5sum`/`SHA1` fields of `Control` (via `debmeta.ParseStanzas`). Runs `dedupe` first.
  - Cache refresh result `Result{Status: "refreshed"}`; staging dir `<StagingDir>/<repoID>/repodata-<unixnano>` (removed on failure; consumed by swap on success).
  - Filters are not applied in cache mode.

- [ ] **Step 1: Failing tests** in `cache_test.go` (reuse `newSyncEnv` / `newDebSyncEnv` with `cfg` setting `m.Mode = store.ModeCache`):
  - `TestCacheRefreshRPM`: 2 upstream pkgs; Run → status `refreshed`; `ListCacheEntries` has both with `Path == Href`; repo `repodata/repomd.xml` bytes == upstream repomd; `repomd.xml.asc` present; zero package downloads (`up.Hits(pkg path) == 0`); `ListPackages` empty.
  - `TestCacheRefreshBadSignature`: wrong GPG key → status `error`, no `cache_entries`, no `repodata`.
  - `TestCacheRefreshCorruptData`: data file sha mismatch → error, previous index untouched.
  - `TestCacheRefreshDeb`: 1 deb → entry Path `pool/main/<file>`, MD5/SHA1 non-empty; `dists/<suite>/InRelease` exists.
  - `TestCacheRefreshIgnoresFilters`: Include `["nomatch"]` → entries still listed.
- [ ] **Step 2: Run** → FAIL. **Step 3: Implement.** **Step 4: Run** `go test ./internal/mirror/` → PASS (existing sync tests unchanged).
- [ ] **Step 5: Commit** `feat(mirror): cache mode index refresh`.

### Task 5: Mirror — on-demand fetch

**Files:**
- Create/modify: `internal/mirror/cache.go`, `internal/mirror/cache_test.go`

**Interfaces:**
- Consumes: `(*Syncer).source`, `downloadVerified` (via `packageSource.Download`), `checkHeaders`, `pipeline.IngestBatch(ctx, slug, 0, files, "cache")`.
- Produces: `(*Syncer) Fetch(ctx context.Context, repo store.Repo, e store.CacheEntry, w io.Writer) error`
  - Per `(repo.ID, e.Filename)` in-flight map on Syncer (mutex + map). Leader: stage to `<StagingDir>/<repoID>/<filename>.part` and `w` together; writes to `w` that fail are dropped (wrap `w` so its first error disables it, never failing the download); download ctx = `context.WithoutCancel(ctx)` + `Timeout` (default 6h); on success rename, `checkHeaders`, `IngestBatch`, remove staged file. Followers: `Download` straight into `w` on the request ctx, no store.
  - Return error only for the caller's view (upstream failure before/while sending). Ingest errors are logged (scrubbed), not returned once bytes were sent.
  - Uses `Syncer.wg` so shutdown waits for in-flight fills.

- [ ] **Step 1: Failing tests:**
  - `TestFetchStoresAndServes`: refresh, Fetch entry into buffer → buffer == body; `ListPackages` has it; repo dir has file; audit `cached`.
  - `TestFetchCorruptNotStored`: `up.Corrupt(p)` → Fetch returns error; no row, no file, no `.part` left.
  - `TestFetchClientGoneStillStores`: writer that errors after 10 bytes and a ctx cancelled at that point → eventually (wait on `s.wg` via a test hook or poll ≤5s) row exists.
  - `TestFetchConcurrent`: upstream handler blocks until 2 Fetch calls started (fake hook); both buffers == body; exactly one row; upstream hits == 2.
  - `TestFetchCredentialsScrubbed`: upstream 500 with token in URL → error text lacks token.
- [ ] **Step 2: Run** → FAIL. **Step 3: Implement.** **Step 4: Run** `go test -race ./internal/mirror/` → PASS.
- [ ] **Step 5: Commit** `feat(mirror): fetch cache misses from upstream on demand`.

### Task 6: Serve — cache lookups, last_pulled, wiring

**Files:**
- Modify: `internal/serve/serve.go`, `internal/serve/apt.go`, `cmd/dnfrepo/main.go` (serve.Deps `Fetch: syncer.Fetch`)
- Test: `internal/serve/cache_test.go`

**Interfaces:**
- Consumes: `store.IsCacheRepo`, `CacheEntryByPath`, `TouchPackage`; `Syncer.Fetch`.
- Produces: `serve.Deps.Fetch func(ctx context.Context, repo store.Repo, e store.CacheEntry, w io.Writer) error` (nil = cache misses 404).
  - Cache repo, path ends `.rpm`/`.deb`: every `/` segment matches `repodataRe`, else 404. `CacheEntryByPath` miss → 404 (no upstream call). Local regular file `<dir>/<e.Filename>` → `ServeContent` then `TouchPackage`. Else set `Content-Type: application/octet-stream`, `Content-Length: e.Size`, and call Fetch with a writer that sends the 200 header on first Write; Fetch error with nothing written → 502 `upstream unavailable` (no detail); success → `TouchPackage` not needed (ingest sets now()).
  - `repodata/*` and `dists/*` served exactly as today. Non-cache repos byte-for-byte unchanged.

- [ ] **Step 1: Failing tests** (fake `Fetch` func + DB):
  - `TestCacheServeLocalTouches`: cached file → 200 body, LastPulled advanced.
  - `TestCacheServeMissFetches`: no file, entry at `Packages/a/a-1.rpm` → GET that path → 200, Fetch called with entry, body from fake.
  - `TestCacheServeUnknownPath404`: path not in entries → 404, Fetch not called.
  - `TestCacheServeUpstreamDown502`: Fetch returns error before writing → 502.
  - `TestCacheServeTraversal404`: `../x.rpm`, `Packages/../../x.rpm` → 404.
  - `TestCacheServeDebPool`: `pool/main/x.deb` miss → Fetch called.
  - `TestCacheServeRespectsAccessMode`: ips-mode repo, denied IP → 403/401 as today, Fetch not called.
- [ ] **Step 2: Run** → FAIL. **Step 3: Implement**, wire `Fetch: syncer.Fetch` in main. **Step 4: Run** `go test ./internal/serve/ && go vet ./...` → PASS.
- [ ] **Step 5: Commit** `feat(serve): pull-through serving for cache mirrors`.

### Task 7: API, UI, docs

**Files:**
- Modify: `internal/api/mirror.go` (`mirrorJSON.Mode`, validate, `ErrModeChange` → 409), `internal/api/packages.go` (`last_pulled` in `pkgJSON`), `internal/api/*_test.go`
- Modify: `web/src/api.ts`, `web/src/pages/MirrorPanel.tsx`, `web/src/pages/RepoDetail.tsx`, `web/src/pages/Help.tsx`, `README.md`
- Test: `internal/api/mirror_test.go`, `web/src/mirrorState.test.ts` (if mode-dependent labels live in `mirrorState.ts`)

**Interfaces:**
- Consumes: Task 1.
- Produces: JSON `mode: "sync" | "cache"` on GET/PUT mirror; `last_pulled` (RFC3339 UTC) on packages.

- [ ] **Step 1: Failing Go tests:** `TestPutMirrorMode` (cache round-trips; `"bogus"` → 400; change with packages → 409); `TestPackagesHaveLastPulled`.
- [ ] **Step 2: Run** → FAIL. **Step 3: Implement API.** **Step 4: Run** `go test ./internal/api/` → PASS.
- [ ] **Step 5: UI:** mode select ("Sync — download everything on schedule" / "Cache — download on first request"); cache hides arches/include/exclude; sync button label "Refresh index" in cache mode; package table for cache repos shows "Last pulled" column sorted desc. Help + README: cache mode section (what it does, keep semantics, filters ignored, index refresh schedule).
- [ ] **Step 6: Run** `cd web && npm test && npx tsc --noEmit && npm run build` → PASS.
- [ ] **Step 7: Commit** `feat(web): cache mode in mirror settings`.

### Task 8: Full verification

- [ ] Run Postgres container; `DNFREPO_TEST_DSN=... go test -race ./... && go vet ./...` → all PASS; web checks PASS; stop container.
- [ ] Update `CLAUDE.md` invariants: cache repos — Rebuild no-op, index only via `pipeline.RefreshCache`, fills via `IngestBatch` batch 0. Commit `docs: cache mode invariants`.
