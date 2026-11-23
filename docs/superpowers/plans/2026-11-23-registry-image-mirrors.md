# Registry Image Mirrors Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The embedded registry can follow an upstream image tag (e.g. `docker.io/plexinc/pms-docker:latest`), copying selected platforms when upstream changes, keeping the last N dated versions.

**Architecture:** New package `internal/imagemirror`: a small registry-v2 pull client over `net/http` (`client.go`), a run that stages blobs on disk then imports them into the embedded registry under its write gate (`mirror.go`), and a 24 h runner (`runner.go`). `internal/registry` gains one write entry point, `Import`. Store tables `registry_mirrors` + `registry_mirror_versions`. Admin API in `internal/api/registry_mirrors.go`, UI section on the Registry page.

**Tech Stack:** Go, pgx/Postgres, Distribution v3.1.2 (`manifest/ocischema`, `manifest/schema2`, `manifest/manifestlist`, storage), `distribution/reference`, React/TS.

**Spec:** `docs/superpowers/specs/2026-11-23-registry-image-mirrors-design.md`

## Global Constraints

- No new Go or npm dependencies.
- Migration is a new file `internal/store/migrations/012_registry_mirrors.sql`; never edit applied ones.
- Default `keep` = 3, must be ≥ 1. Dated tag: `<tag>-YYYYMMDD` (UTC), then `-2`, `-3`… same day.
- Credentials sealed with `secretbox` (`SECRET_KEY`); never in logs or responses (`has_credentials` only). Credentials given while `SECRET_KEY` unset → 400.
- New `registry_repos` row: public + unlisted; for a mirror with credentials: mode `users`, no grants. An existing row is never changed.
- Selected platform missing upstream → run fails, error names it, previous copy untouched.
- Nothing filtered (no platforms selected, or all upstream platforms selected) → index bytes stored unchanged, attestation entries copied, local digest == upstream digest. Subset → attestation entries dropped, new index.
- Download before `gate.Begin()`; only the import runs under the gate. Staging: `<DATA_DIR>/image-staging/<run>/`, removed after each run and cleared at startup.
- Mirror owns `local_name:tag`: up to date only if upstream digest == `upstream_digest` AND local tag digest == newest ledger digest.
- Only tags in `registry_mirror_versions` are ever pruned.
- `PUT` edits only platforms, keep, credentials; platform change clears `upstream_digest` and starts a run.
- Conventional commits; TDD (failing test first); commit per task only after the user approves committing (CLAUDE.md).
- Tests: `DNFREPO_TEST_DSN=postgres://postgres:pw@localhost:5433/postgres go test ./...`, `go vet ./...`; web: `cd web && npm test && npx tsc --noEmit && npm run build`.

## Review Focus

1. Docker Hub token flow: `WWW-Authenticate: Bearer realm=…,service=…,scope=…`; anonymous and with Basic creds against the realm; plain `Basic` challenge. Expect: both work; a 401 from the token realm surfaces as "upstream auth failed", without the token.
2. Partial failure mid-import (blob missing, manifest put fails): the moving tag and ledger must be unchanged; staging removed.
3. GC running at import time: run reports "registry busy (garbage collection)" and stores nothing; GC is never held for the download.
4. A pushed `local_name:tag` over the mirror's copy: next run re-copies even though upstream didn't change.
5. Same-day second change: dated tag `-2`; retention with keep=1 deletes the older dated tag but never a pushed look-alike tag not in the ledger.

---

### Task 1: Store — mirrors and version ledger

**Files:**
- Create: `internal/store/migrations/012_registry_mirrors.sql`, `internal/store/registry_mirrors.go`
- Test: `internal/store/registry_mirrors_test.go`

**Interfaces:**
- Produces:
  ```go
  type ImageMirror struct {
      ID int64; Upstream, Tag, LocalName string; Platforms []string; Keep int
      Username string; Secret []byte // sealed token, nil = none
      UpstreamDigest string; LastRun *time.Time; LastStatus, LastError string; CreatedAt time.Time
  }
  type ImageVersion struct{ MirrorID int64; Tag, Digest, UpstreamDigest string; CreatedAt time.Time }
  var ErrMirrorExists = errors.New("an image mirror for that name and tag already exists")
  func (s *Store) AddImageMirror(ctx, m ImageMirror) (ImageMirror, error)           // ErrMirrorExists on (local_name, tag) clash
  func (s *Store) ImageMirrorByID(ctx, id int64) (ImageMirror, error)             // ErrNotFound
  func (s *Store) ListImageMirrors(ctx) ([]ImageMirror, error)                    // by id
  func (s *Store) UpdateImageMirror(ctx, id int64, platforms []string, keep int, username *string, secret []byte, clearDigest bool) error // username nil = keep creds
  func (s *Store) DeleteImageMirror(ctx, id int64) error                          // cascades versions
  func (s *Store) RecordImageMirrorRun(ctx, id int64, status, errMsg string) error
  func (s *Store) AddImageVersion(ctx, v ImageVersion, upstreamDigest string) error // inserts row AND sets mirror.upstream_digest, one tx
  func (s *Store) ImageVersions(ctx, mirrorID int64) ([]ImageVersion, error)      // newest first
  func (s *Store) DeleteImageVersion(ctx, mirrorID int64, tag string) error
  func (s *Store) EnsureRegistryRepoMode(ctx, name string, mode AccessMode) error // INSERT … ON CONFLICT DO NOTHING
  ```
- Schema per spec "Data": `platforms text[] NOT NULL DEFAULT '{}'`, `keep int NOT NULL DEFAULT 3 CHECK (keep >= 1)`, `UNIQUE (local_name, tag)`, versions PK `(mirror_id, tag)`, FK `ON DELETE CASCADE`.

- [ ] **Step 1: Write failing tests** in `registry_mirrors_test.go` (skip without DSN, like other store tests):
  `TestImageMirrorCRUD` (add → by id round-trips Platforms/Keep/Secret; duplicate (local_name, tag) → `ErrMirrorExists`; update with `username=nil` keeps Secret; `clearDigest` empties `UpstreamDigest`; delete removes versions);
  `TestImageVersionsLedger` (AddImageVersion sets mirror `UpstreamDigest`; ImageVersions newest first; DeleteImageVersion);
  `TestEnsureRegistryRepoModeKeepsExisting` (existing row stays `public` when called with `users`; new name gets `users`).
- [ ] **Step 2: Run** `DNFREPO_TEST_DSN=… go test ./internal/store -run 'ImageMirror|ImageVersions|EnsureRegistryRepoMode'` — Expected: FAIL (undefined).
- [ ] **Step 3: Implement** migration and `registry_mirrors.go` (follow `sources.go` scan/cols pattern; map unique violation `23505` to `ErrMirrorExists` with the existing `pgCode` helper).
- [ ] **Step 4: Run** same command — Expected: PASS; then `go test ./internal/store` all PASS.
- [ ] **Step 5: Commit** `feat(store): registry image mirror tables`

### Task 2: Upstream pull client

**Files:**
- Create: `internal/imagemirror/client.go`, `internal/imagemirror/fake_test.go` (test upstream)
- Test: `internal/imagemirror/client_test.go`

**Interfaces:**
- Produces:
  ```go
  type Ref struct{ Host, Repo, Tag string } // Host is the API host (docker.io → registry-1.docker.io)
  func ParseRef(s string) (Ref, error)      // reference.ParseNormalizedNamed; default tag latest; digest refs → error
  func (r Ref) String() string               // "docker.io/plexinc/pms-docker:latest" (display host, not API host)
  func (r Ref) DefaultLocalName() string     // path without host: "plexinc/pms-docker", "library/alpine"
  type Client struct{ HTTP *http.Client; Username, Token string; Scheme string /* "https"; tests "http" */ }
  func (c *Client) Head(ctx, r Ref) (digest string, err error)
  func (c *Client) Manifest(ctx, r Ref, reference string) (mediaType string, payload []byte, err error) // verifies sha256 when reference is a digest
  func (c *Client) Blob(ctx, r Ref, dg string, dst string) (size int64, err error)                     // writes file, verifies digest, removes on mismatch
  var ErrAuth = errors.New("upstream auth failed")
  ```
  Accept header: OCI index, OCI manifest, Docker manifest list, Docker schema2. Schema1 media type → error "schema1 manifests are not supported". Token: on 401 parse `WWW-Authenticate`; `Bearer` → GET realm with `service` + `scope=repository:<repo>:pull` (Basic creds if set), cache token in Client for the run; `Basic` → retry with Basic creds. Token realm non-200 → `ErrAuth` (wrapped, no secret in message).
- Fake upstream (`fake_test.go`): `httptest` server holding repos `map[repo]map[ref][]byte` + blobs; option `Bearer bool` (serves a `/token` realm and requires the token), `User/Pass`. Helpers `fakeImage(t, platforms ...string) (indexDigest string)` building real OCI index + child manifests + config/layer blobs, with an `unknown/unknown` attestation entry.

- [ ] **Step 1: Write failing tests:** `TestParseRef` (`plexinc/pms-docker` → host `registry-1.docker.io`, repo `plexinc/pms-docker`, tag `latest`; `alpine` → `library/alpine`; `ghcr.io/a/b:1` → host `ghcr.io`; `x@sha256:…` → error); `TestClientBearerAnonymous` and `TestClientBearerWithCreds` (Head returns index digest); `TestClientBadCredsIsErrAuth` (errors.Is ErrAuth, message lacks the token); `TestBlobDigestMismatchRemovesFile`; `TestManifestSchema1Rejected`.
- [ ] **Step 2: Run** `go test ./internal/imagemirror` — Expected: FAIL (undefined).
- [ ] **Step 3: Implement** `client.go`.
- [ ] **Step 4: Run** — Expected: PASS.
- [ ] **Step 5: Commit** `feat(imagemirror): registry v2 pull client`

### Task 3: Registry import entry point

**Files:**
- Modify: `internal/registry/inspect.go` (or new `internal/registry/import.go`)
- Test: `internal/registry/import_test.go`

**Interfaces:**
- Produces:
  ```go
  type ImportBlob struct{ Digest string; Size int64; MediaType, Path string } // Path: staged file
  type ImportManifest struct{ MediaType string; Payload []byte }               // children before the index
  // Import writes blobs (skipping ones present), then manifests in order, then points each of
  // tags at the last manifest — all under the write gate. Returns ErrReadOnly when GC holds the gate.
  // Tags move only after every blob and manifest is stored.
  func (r *Registry) Import(ctx context.Context, name string, blobs []ImportBlob, manifests []ImportManifest, tags []string) (digest string, err error)
  func (r *Registry) TagDigest(ctx context.Context, name, tag string) (string, error) // ErrNoSuchRepo / ErrNoSuchTag
  ```
  `DeleteTag` (existing) is the untag used by pruning. Import `distribution.UnmarshalManifest`; blank-import `manifest/ocischema`, `manifest/schema2`, `manifest/manifestlist` so they are registered.

- [ ] **Step 1: Write failing tests:** `TestImportThenPull` (import an OCI image built in the test; `Tags`/`TagDigest` show it; tag digest == sha256 of the index payload); `TestImportRefusedDuringGC` (close the gate via the existing test hook / `gate.Close`; Import → `ErrReadOnly`, no tag); `TestImportMissingBlobLeavesTag` (existing tag t → import whose manifest references an un-staged blob fails; t still points at the old digest).
- [ ] **Step 2: Run** `go test ./internal/registry -run 'Import|TagDigest'` — Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./internal/registry` — Expected: PASS.
- [ ] **Step 5: Commit** `feat(registry): import entry point for mirrored images`

### Task 4: Mirror run

**Files:**
- Create: `internal/imagemirror/mirror.go`
- Test: `internal/imagemirror/mirror_test.go` (fake upstream + real `registry.New` in a temp dir + DB)

**Interfaces:**
- Consumes: Task 1 store funcs, Task 2 `Client`/`Ref`, Task 3 `Import`/`TagDigest`/`DeleteTag`.
- Produces:
  ```go
  type Mirrors struct {
      Store *store.Store; Reg *registry.Registry; Box *secretbox.Box
      HTTP *http.Client; StagingDir string; Scheme string // "" = https
      Now func() time.Time                                 // nil = time.Now (tests pin dates)
  }
  func (m *Mirrors) RunOne(ctx context.Context, id int64) error   // records status on the row; "already running" if in flight
  func (m *Mirrors) Inspect(ctx context.Context, upstream, username, token string) (digest string, platforms []string, err error)
  func (m *Mirrors) InspectMirror(ctx context.Context, id int64) (string, []string, error) // uses sealed creds
  const StatusUpToDate, StatusDownloaded = "up to date", "downloaded"
  ```
  Platform strings `os/arch[/variant]`; attestation = platform `unknown/unknown` or annotation `vnd.docker.reference.type`. Subset index: unmarshal payload into `map[string]any`, filter `manifests`, marshal (keeps mediaType/annotations). Dated tag: first free of `<tag>-YYYYMMDD`, `-2`, … among existing tag names. After import: `AddImageVersion`, prune `ImageVersions[keep:]` via `DeleteTag` + `DeleteImageVersion`, `EnsureRegistryRepoMode(local, users-if-creds-else-public)`.

- [ ] **Step 1: Write failing tests:**
  `TestRunCopiesAllPlatformsDigestMatches` (no platforms selected → local `latest` digest == upstream index digest, dated tag `latest-20261123`, status downloaded, registry_repos row public);
  `TestRunSecondTimeUpToDate` (no blob requests on 2nd run; status up to date);
  `TestRunSubsetDropsAttestation` (platforms `[linux/amd64]` → local index has 1 entry, digest ≠ upstream);
  `TestRunMissingPlatformFailsKeepsOld` (select `linux/arm/v7` absent → error names it, old tag unchanged);
  `TestRunRetentionAndSameDay` (keep=2, three upstream changes same day → tags `-20261123`, `-2`, `-3`; oldest pruned; a pushed tag `latest-20260101` survives);
  `TestRunRecopiesAfterPushOverTag` (push over `latest` via `registrytest.Push`; next run copies again);
  `TestRunGCBusy` (gate closed → status error "registry busy (garbage collection)");
  `TestCredentialedMirrorDefaultsToUsers` (mirror with creds → new registry_repos row mode users);
  `TestStagingRemovedAfterRun` (StagingDir empty after success and after failure).
- [ ] **Step 2: Run** `DNFREPO_TEST_DSN=… go test ./internal/imagemirror -run Run|Credentialed|Staging` — Expected: FAIL.
- [ ] **Step 3: Implement** `mirror.go`.
- [ ] **Step 4: Run** `go test ./internal/imagemirror` — Expected: PASS.
- [ ] **Step 5: Commit** `feat(imagemirror): copy upstream images with platform filter and retention`

### Task 5: Runner and wiring

**Files:**
- Create: `internal/imagemirror/runner.go`
- Modify: `cmd/dnfrepo/main.go` (inside the `cfg.RegistryDir != ""` block)
- Test: `internal/imagemirror/runner_test.go`

**Interfaces:**
- Produces: `func (m *Mirrors) RunAll(ctx)` (each mirror, log and carry on); `func (m *Mirrors) Start(ctx, interval time.Duration)` (clears StagingDir, first run after 1 min, then every interval; default 24 h).
- Ruling vs spec: spec says `sources.Scheduler` gains the runner; the registry is built after the scheduler starts and `sources` must not import `registry`, so the runner lives in `imagemirror` with the same cadence. Behaviour identical.

- [ ] **Step 1: Failing test** `TestStartClearsStaging` (stale file in StagingDir removed by Start before the first run).
- [ ] **Step 2: Run** — Expected: FAIL.
- [ ] **Step 3: Implement**; in `main.go` build `&imagemirror.Mirrors{Store: st, Reg: reg, Box: box, HTTP: &http.Client{Transport: tr}, StagingDir: filepath.Join(cfg.DataDir, "image-staging")}` and `Start(ctx, 0)` (move the `box`/`tr` setup above the registry block if needed); pass it to `api.RegistryAPI` as `Mirrors`.
- [ ] **Step 4: Run** `go build ./... && go vet ./... && go test ./internal/imagemirror` — Expected: PASS.
- [ ] **Step 5: Commit** `feat(imagemirror): daily runner`

### Task 6: Admin API

**Files:**
- Create: `internal/api/registry_mirrors.go`
- Modify: `internal/api/registry.go` (field `Mirrors *imagemirror.Mirrors` on `RegistryAPI`; register routes)
- Test: `internal/api/registry_mirrors_test.go`

**Interfaces:**
- Routes (admin auth like other registry routes):
  `GET /api/registry/mirrors` → `[{id, upstream, tag, local_name, platforms, keep, has_credentials, upstream_digest, last_run, last_status, last_error}]`;
  `POST /api/registry/mirrors` `{upstream, local_name?, platforms?, keep?, username?, token?}` → 201 + row, then `go RunOne` detached (`context.WithoutCancel`);
  `PUT /api/registry/mirrors/{id}` `{platforms, keep, username?, token?}` (username omitted = keep creds; `""` = clear);
  `DELETE /api/registry/mirrors/{id}` (local image kept);
  `POST /api/registry/mirrors/{id}/run` → 202;
  `POST /api/registry/mirrors/inspect` `{upstream, username?, token?}` or `{id}` → `{digest, platforms}`.
- Errors: bad ref / keep < 1 / bad local name → 400; creds without `SECRET_KEY` → 400; `ErrMirrorExists` → 409; unknown id → 404; upstream errors on inspect → 502 with message (no secrets).

- [ ] **Step 1: Failing tests:** `TestMirrorAPICreateListNoSecret` (response has `has_credentials:true`, body never contains the token); `TestMirrorAPIDuplicate409`; `TestMirrorAPICredsWithoutKey400`; `TestMirrorAPIPutPlatformsClearsDigestAndRuns`; `TestMirrorAPIInspectByID` (fake upstream with Basic creds works via stored creds).
- [ ] **Step 2: Run** `DNFREPO_TEST_DSN=… go test ./internal/api -run MirrorAPI` — Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./internal/api` — Expected: PASS.
- [ ] **Step 5: Commit** `feat(api): registry image mirror endpoints`

### Task 7: UI

**Files:**
- Modify: `web/src/registry.ts` (types + pure helpers), `web/src/api.ts`, `web/src/pages/Registry.tsx`
- Test: `web/src/registry.test.ts`

**Interfaces:**
- `type ImageMirror = {id, upstream, tag, local_name, platforms: string[], keep, has_credentials, upstream_digest, last_run: string|null, last_status, last_error}`.
- `function defaultLocalName(ref: string): string` (mirrors `Ref.DefaultLocalName` for prefill: strip host when first segment has `.`/`:` or is `localhost`, strip tag; bare name → `library/<name>`).
- api: `imageMirrors`, `addImageMirror`, `updateImageMirror`, `deleteImageMirror`, `runImageMirror`, `inspectUpstream`.
- Registry page: **Mirrors** table + **Add mirror** form per spec "UI"; *Load platforms* fills checkboxes; edit reuses the form with upstream/local name read-only.

- [ ] **Step 1: Failing test** for `defaultLocalName` (`docker.io/plexinc/pms-docker:latest` → `plexinc/pms-docker`; `alpine` → `library/alpine`; `ghcr.io/a/b:1` → `a/b`; `localhost:5000/x` → `x`).
- [ ] **Step 2: Run** `cd web && npx vitest run src/registry.test.ts` — Expected: FAIL.
- [ ] **Step 3: Implement** helpers, api calls, page section.
- [ ] **Step 4: Run** `npm test && npx tsc --noEmit && npm run build` — Expected: PASS.
- [ ] **Step 5: Commit** `feat(web): registry image mirrors UI`

### Task 8: Docs

**Files:** `README.md` (Container registry: "Image mirrors" bullets; API table rows), `docs/api.md`, `CLAUDE.md` (layout: `imagemirror`; invariant: mirrors write only through `Registry.Import` under the gate, prune only ledger tags, creds sealed), `web/src/pages/Help.tsx` if it documents the registry.

- [ ] **Step 1:** Write the docs.
- [ ] **Step 2: Run** full suite: `DNFREPO_TEST_DSN=… go test ./... && go vet ./...` and web checks — Expected: PASS.
- [ ] **Step 3: Commit** `docs: registry image mirrors`
