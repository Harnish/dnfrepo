# Mirror Repos Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A repo can be a mirror of an upstream dnf repo (URL or pasted `.repo`), synced on a schedule or on demand with filters, recording each sync as a batch that can be promoted as a unit.

**Architecture:** New package `internal/mirror` (parse `.repo`, read upstream metadata, plan, schedule, sync). `pipeline` stays the only writer of package files and gains `IngestBatch` (many files, one purge, one rebuild) and `PromoteBatch`. State lives in new tables (`mirrors`, `sync_batches`) plus `packages.origin_batch_id`. A sync stages and verifies every file outside the repo, then publishes once.

**Tech Stack:** Go, PostgreSQL (pgx), `createrepo_c`, React/TypeScript. No new modules: promote `github.com/klauspost/compress` (zstd) and `github.com/ulikunitz/xz` from indirect to direct; `github.com/ProtonMail/go-crypto` is already direct.

**Spec:** `docs/superpowers/specs/2026-11-10-mirror-repos-design.md` (read it first; this plan does not repeat its rationale).

## Global Constraints

- Backend Go, PostgreSQL. `pipeline` is the ONLY writer of package files, under the per-repo mutex. Next migration number is `005` (add files, never edit applied ones).
- Metadata swap stays atomic. Sync and rebuild run detached from request contexts (`context.WithoutCancel`).
- A package enters a non-first stage ONLY by promotion. A mirror repo is a first stage, accepts no uploads and no `sources`, and can never be linked behind another repo.
- Upstream credentials: stored AES-GCM-sealed with a key derived from env `SECRET_KEY` (sha256 of the string); never logged, never returned by any API response, never in errors. If `SECRET_KEY` is unset, credentials are rejected with 400.
- A sync never deletes a package; only retention purges. Whole-sync atomicity: all files staged and verified, or nothing published.
- Tests: TDD (failing test first). DB tests: `DNFREPO_TEST_DSN=postgres://postgres:pw@localhost:5433/postgres go test ./...` against `podman run -d --rm --name dnfrepo-pg -p 5433:5432 -e POSTGRES_PASSWORD=pw postgres:17-alpine` (stop it when done). Also `go vet ./...`. Web: `cd web && npm test && npx tsc --noEmit && npm run build`. No real network in tests: fake upstreams via `httptest`.
- Commits are conventional (`feat:`, `fix:`, `chore:`). **Ask the user once at the start whether the executor may commit after each task; never commit without that answer.**
- Update docs as you go: the final task checks them, but each task that changes behavior notes it for Task 14.

## Review Focus

Failure modes the spec implies; each has a pinning test in the owning task.

1. Hostile upstream `<location href>` (`../../x.rpm`, absolute URL, percent-encoded `..`): rejected; nothing is written outside staging or the repo dir. (Task 5)
2. A download dies midway or fails its checksum: repo and metadata unchanged, and a retry reuses the files already staged. (Task 8)
3. Retention loop: upstream has 5 versions, repo keep is 3: only the newest 3 are downloaded, and a second sync downloads nothing. (Tasks 6, 8)
4. Process killed mid-sync, or **Sync now** while one is running: state is never stuck `running`; the second request gets 409. (Tasks 9, 11)
5. Schedule at a local time that does not exist or happens twice on a DST change, and monthly day 31 in a short month. (Task 9)

---

### Task 1: Schema and mirror store

**Files:**
- Create: `internal/store/migrations/005_mirrors.sql`, `internal/store/mirrors.go`, `internal/store/mirrors_test.go`

**Interfaces:**
- Produces (package `store`):
  ```go
  type Schedule struct { Frequency, TimeOfDay, Timezone string; Weekdays []int; DayOfMonth int; Paused bool } // Frequency: "manual","daily","weekly","monthly"; TimeOfDay "HH:MM"; Weekdays 0=Sunday
  type Mirror struct {
      RepoID int64; UpstreamURL, Username, GPGKey string; TokenEnc []byte
      RequireSig bool; Arches, Include, Exclude []string; KeepLatest int
      Schedule Schedule; State, LastStatus, LastError string // State "idle"|"running"
      LastRun, NextRun *time.Time
  }
  var ErrMirrorRepo = errors.New("repo is a mirror")          // uploads, sources, linking refused
  var ErrNotEligible = errors.New("only an empty first-stage repo with no sources can become a mirror")
  var ErrBusy = errors.New("sync already running")
  func (s *Store) SetMirror(ctx context.Context, m Mirror) error        // upsert config fields; on first create enforces eligibility (ErrNotEligible)
  func (s *Store) MirrorByRepo(ctx context.Context, repoID int64) (Mirror, error) // ErrNotFound
  func (s *Store) DeleteMirror(ctx context.Context, repoID int64) error
  func (s *Store) IsMirror(ctx context.Context, repoID int64) (bool, error)
  func (s *Store) ClaimMirror(ctx context.Context, repoID int64) error  // idle→running; ErrBusy if running; ErrNotFound if none
  func (s *Store) ClaimDueMirror(ctx context.Context, now time.Time) (*Mirror, error) // atomically claims one idle, unpaused mirror whose next_run <= now; nil if none
  func (s *Store) FinishMirror(ctx context.Context, repoID int64, status, errMsg string, next *time.Time) error // running→idle, sets last_run=now()
  func (s *Store) InterruptRunning(ctx context.Context) (int, error)    // running→idle with last_status "interrupted"; returns count
  ```
  Also guards: `AddSource` returns `ErrMirrorRepo` for a mirror repo; `SetPrevious(ctx, id, &prev)` returns `ErrMirrorRepo` when `id` is a mirror.

- [ ] **Step 1: Write failing tests** in `mirrors_test.go` (package `store_test`, use `storetest.New` and the `mustRepo` helper): `TestSetMirrorRequiresEmptyFirstStageRepo` (repo with a package, with a source, or with `previous` → `ErrNotEligible`; empty first stage → ok), `TestMirrorRoundTrip` (all fields incl. `[]string` and `Schedule` come back; `TokenEnc` bytes preserved), `TestClaimMirrorIsExclusive` (second claim → `ErrBusy`; after `FinishMirror` claimable again, `LastStatus`/`NextRun` stored), `TestClaimDueMirrorSkipsPausedAndFuture`, `TestInterruptRunning` (running → idle, `LastStatus=="interrupted"`, count 1), `TestMirrorRepoRejectsSourceAndLinking` (`AddSource` and `SetPrevious` → `ErrMirrorRepo`).
- [ ] **Step 2: Run** `go test ./internal/store/ -run Mirror` → FAIL (undefined).
- [ ] **Step 3: Implement.** Migration creates `mirrors(repo_id bigint PRIMARY KEY REFERENCES repos ON DELETE CASCADE, ...)` with columns for every `Mirror` field (`text[]` for lists, `weekdays int[]`, `state text NOT NULL DEFAULT 'idle'`). `ClaimDueMirror` is a single `UPDATE ... WHERE repo_id=(SELECT ... FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING`. Eligibility check and insert in one transaction with `FOR SHARE` on the repo row, like `AddPackageFirstStage`.
- [ ] **Step 4: Run** `go test ./internal/store/` → PASS.
- [ ] **Step 5: Commit** `feat(store): mirrors table and claim/finish state`.

### Task 2: Batches and package origin

**Files:**
- Create: `internal/store/batches.go`, `internal/store/batches_test.go`, `internal/store/migrations/006_batches.sql`
- Modify: `internal/store/packages.go` (`Package`, `pkgCols`, `scanPkg`, `AddPackagesAudited`)

**Interfaces:**
- Produces:
  ```go
  type Batch struct { ID, RepoID int64; StartedAt time.Time; FinishedAt *time.Time; Status string; AddedCount int; Error string }
  func (s *Store) CreateBatch(ctx context.Context, repoID int64) (int64, error)
  func (s *Store) FinishBatch(ctx context.Context, id int64, status string, added int, errMsg string) error
  func (s *Store) ListBatches(ctx context.Context, repoID int64) ([]Batch, error) // newest first, max 100
  func (s *Store) BatchFilenames(ctx context.Context, repoID, batchID int64) ([]string, error) // packages in repoID with origin_batch_id=batchID, sorted
  func (s *Store) AddMirrorPackages(ctx context.Context, repoID, batchID int64, pkgs []Package, actor string) error // one tx: first-stage check (ErrLaterStage), insert with origin_batch_id, one "synced" audit event per package; ErrExists on duplicate
  ```
  `Package` gains `OriginBatchID *int64`; `AddPackagesAudited` copies `p.OriginBatchID` onto the new rows (this is how a batch follows packages through promotion).
- Design note (deviation from the spec's `batch_packages` table): `packages.origin_batch_id` already answers "which packages belong to batch N in this repo", so no `batch_packages` table. `added_count` on the batch keeps the number after retention purges rows.

- [ ] **Step 1: Failing tests:** `TestBatchLifecycle` (create → finish → listed with count/status), `TestAddMirrorPackagesSetsOriginAndAudits`, `TestOriginBatchSurvivesPromotionInsert` (rows added through `AddPackagesAudited` from packages carrying an `OriginBatchID` keep it), `TestBatchFilenamesScopedToRepo`.
- [ ] **Step 2: Run** `go test ./internal/store/ -run Batch` → FAIL.
- [ ] **Step 3: Implement.** Migration adds `sync_batches` and `ALTER TABLE packages ADD COLUMN origin_batch_id bigint REFERENCES sync_batches(id) ON DELETE SET NULL`. Update `pkgCols`/`scanPkg`/inserts.
- [ ] **Step 4: Run** `go test ./internal/store/ ./internal/pipeline/` → PASS (existing promote tests still green).
- [ ] **Step 5: Commit** `feat(store): sync batches and package origin`.

### Task 3: Secret box and config

**Files:**
- Create: `internal/secretbox/secretbox.go`, `internal/secretbox/secretbox_test.go`
- Modify: `internal/config/config.go`, `internal/config/config_test.go`

**Interfaces:**
- Produces:
  ```go
  package secretbox
  type Box struct{ /* AES-GCM */ }
  func New(key string) (*Box, error)                // error if key == ""; key = sha256(key)
  func (b *Box) Seal(plain string) ([]byte, error)  // random nonce prepended
  func (b *Box) Open(sealed []byte) (string, error) // error on tamper or wrong key
  ```
  `config.Config` gains `SecretKey string` (env `SECRET_KEY`, optional) and `RebuildTimeout time.Duration` (env `REBUILD_TIMEOUT` as a Go duration, default `10m`; invalid → `Load` error).

- [ ] **Step 1: Failing tests:** `TestSealOpenRoundTrip`, `TestOpenRejectsTamperAndWrongKey`, `TestSealIsNonDeterministic` (two seals of the same text differ), `TestNewRejectsEmptyKey`; config: `TestLoadSecretKeyAndRebuildTimeout` (default 10m, override `30m`, `abc` → error).
- [ ] **Step 2–4:** run → FAIL, implement (stdlib `crypto/aes`, `cipher.NewGCM`), run → PASS.
- [ ] **Step 5: Commit** `feat: secretbox for upstream credentials; SECRET_KEY and REBUILD_TIMEOUT config`.

### Task 4: `.repo` parsing and variable resolution

**Files:**
- Create: `internal/mirror/repofile.go`, `internal/mirror/repofile_test.go`

**Interfaces:**
- Produces (package `mirror`):
  ```go
  type RepoSection struct { ID, Name, BaseURL, Metalink, MirrorList, GPGKey string; RepoGPGCheck, Enabled bool; Username, Password string }
  func ParseRepoFile(text string) ([]RepoSection, error) // INI; error if no section has baseurl/metalink/mirrorlist; unknown keys ignored; baseurl may be multi-line (first URL wins)
  func Variables(s string) []string                      // distinct $name / ${name} used in s, sorted
  func ResolveVars(s string, vars map[string]string) (string, []string) // substitutes; second result = names still missing
  func ResolveMetalink(ctx context.Context, c *http.Client, metalinkURL string) (string, error) // first <url> ending in "repodata/repomd.xml", returned with that suffix removed
  func ResolveMirrorList(ctx context.Context, c *http.Client, listURL string) (string, error)    // first non-comment, non-empty line
  ```

- [ ] **Step 1: Failing tests:** `TestParseFedoraRepoFile` (a two-section file modeled on `fedora.repo`: `metalink=https://mirrors.fedoraproject.org/metalink?repo=fedora-$releasever&arch=$basearch`, `gpgkey=file:///etc/pki/...`, `enabled=1`; assert sections, ids, fields), `TestParseDnfrepoUsersModeFile` (`baseurl`, `username`, `password`, `repo_gpgcheck=1`, `gpgkey=https://.../gpg.key`), `TestParseRejectsFileWithoutURL`, `TestVariablesAndResolve` (`$releasever`, `${basearch}`, missing reported, `$$` untouched), `TestResolveMetalinkPicksFirstRepomdURL` and `TestResolveMirrorList` using `httptest`.
- [ ] **Step 2–4:** run → FAIL, implement with `bufio`/`strings` (no INI library), `encoding/xml` for the metalink, run → PASS.
- [ ] **Step 5: Commit** `feat(mirror): parse .repo files and resolve variables/metalinks`.

### Task 5: Upstream metadata and downloads

**Files:**
- Create: `internal/mirror/upstream.go`, `internal/mirror/upstream_test.go`, `internal/mirror/fakeupstream_test.go` (shared test helper)
- Modify: `internal/pipeline/pipeline.go` (add exported `ValidFilename(string) bool` using the existing `filenameRe`), `go.mod`

**Interfaces:**
- Consumes: `pipeline.ValidFilename`.
- Produces:
  ```go
  type UpstreamPkg struct { Name, Arch, Version, Release, Filename, Href, SHA256 string; Size int64 }
  type Upstream struct { Base string; Client *http.Client; Username, Token, GPGKey string; RequireSig bool; MaxRPMBytes int64 }
  var ErrChecksum = errors.New("checksum mismatch")
  func (u *Upstream) Packages(ctx context.Context) ([]UpstreamPkg, error)
  func (u *Upstream) Download(ctx context.Context, p UpstreamPkg, dst io.Writer) error // streams, enforces Size and SHA256 and MaxRPMBytes; ErrChecksum on mismatch
  ```
  Test helper (same package, `_test.go`): `newFakeUpstream(t, opts) *fakeUpstream` serving `repodata/repomd.xml` (+ `.asc` signed by a throwaway key), `primary.xml.gz` (also `.zst`/`.xz` variants), and rpm bodies, with `URL()`, `AddPkg(name, ver, rel, arch string, body []byte)`, hooks to corrupt a body, drop a file, or require Basic auth.

- [ ] **Step 1: Failing tests:** `TestPackagesParsesPrimaryGzZstXz`; `TestPackagesVerifiesMetadataChecksum` (tampered primary → error); `TestSignatureRequiredAndVerified` (valid `.asc` ok; bad or missing with `RequireSig` → error; key set but `.asc` absent → error); `TestBasicAuthSentWhenCredentialsSet`; `TestDownloadVerifiesSizeAndSHA256` (`ErrChecksum`; body larger than `Size` or `MaxRPMBytes` aborted); `TestUnsupportedChecksumTypeRejected`; **`TestHostileLocationHrefRejected`** (`../../x.rpm`, `/abs.rpm`, `http://evil/x.rpm`, `%2e%2e/x.rpm`, a basename failing `ValidFilename`: each makes `Packages` return an error naming the entry, never a package); `TestPrimaryDecompressionCapped` (output larger than 1 GiB limit aborts; use a small injected limit field for the test).
- [ ] **Step 2–4:** run → FAIL, implement. Verify with `openpgp.CheckArmoredDetachedSignature` (already a dependency via `signer`). Decode `.gz` with `compress/gzip`, `.zst` with `klauspost/compress/zstd`, `.xz` with `ulikunitz/xz`; `encoding/xml` streaming decode; `Filename = path.Base(Href)`. Run `go mod tidy`. Run → PASS.
- [ ] **Step 5: Commit** `feat(mirror): read and verify upstream repo metadata`.

### Task 6: Planner (filters and retention)

**Files:**
- Create: `internal/mirror/plan.go`, `internal/mirror/plan_test.go`

**Interfaces:**
- Consumes: `UpstreamPkg`, `store.Package`, `rpmver.ComparePackage(v1, r1, v2, r2 string) int`.
- Produces:
  ```go
  type Filter struct { Arches, Include, Exclude []string; KeepLatest int } // globs via path.Match on package name; empty Arches/Include = all
  var ErrConflict = errors.New("upstream file has a different checksum than the repo's file of the same name")
  func Plan(up []UpstreamPkg, have []store.Package, f Filter, repoKeep int, overrides map[string]int) ([]UpstreamPkg, error)
  ```
  Semantics: after filtering, per `(name, arch)` the effective limit is the smallest positive of `f.KeepLatest` and the repo's keep (override beats default); only candidates that would survive that limit **together with what the repo already has** are returned, minus those already present (same filename and checksum). Present filename with a different checksum → `ErrConflict`. `noarch` is always allowed regardless of `Arches`. Result order is deterministic (name, arch, filename).

- [ ] **Step 1: Failing tests:** `TestPlanArchIncludeExclude`; `TestPlanNoarchAlwaysAllowed`; `TestPlanKeepLatestPerNameArch`; **`TestPlanNoRedownloadAfterRetention`** (upstream v1..v5, repo keep 3, repo has v3–v5 → empty plan; empty repo → plan is v3–v5 only); `TestPlanSkipsPresentSameChecksum`; `TestPlanConflictOnDifferentChecksum`; `TestPlanOverrideBeatsDefault`.
- [ ] **Step 2–4:** run → FAIL, implement, run → PASS.
- [ ] **Step 5: Commit** `feat(mirror): sync planner with filters and retention-aware selection`.

### Task 7: Pipeline batch ingest, promote by batch, mirror guards

**Files:**
- Modify: `internal/pipeline/pipeline.go` (add `ErrMirrorRepo` mapping, `IngestBatch`, `RebuildTimeout`), `internal/pipeline/promote.go` (add `PromoteBatch`)
- Test: `internal/pipeline/batch_test.go` (reuse `newSvc`, `mkRepo`, `rpmFile`, `fakePub` from the existing tests)

**Interfaces:**
- Consumes: `store.AddMirrorPackages`, `store.BatchFilenames`, `store.IsMirror`, `store.ErrMirrorRepo`.
- Produces:
  ```go
  type StagedFile struct { Path, Filename, SHA256 string; Size int64 } // Path is a verified file outside the repo dir
  func (s *Service) IngestBatch(ctx context.Context, slug string, batchID int64, files []StagedFile, actor string) ([]store.Package, error)
  func (s *Service) PromoteBatch(ctx context.Context, fromSlug string, batchID int64, actor string) (PromoteResult, error)
  // Service gains: RebuildTimeout time.Duration (0 means 10m); Upload now returns store.ErrMirrorRepo for mirror repos (the API maps it to 409).
  ```
  `IngestBatch`: under the repo lock, `rpmmeta.Parse` each staged file, hard link (fallback copy via `linkOrCopy`) into the repo dir without overwriting, `AddMirrorPackages`, then `purgeLocked` and ONE `rebuild`. Any failure removes files it placed and returns the error with the repo unchanged. `PromoteBatch` resolves `BatchFilenames` (none → `ErrEmptyPromotion`) and calls `Promote`.

- [ ] **Step 1: Failing tests:** `TestIngestBatchRebuildsOnce` (3 files → fake publisher counts 1 rebuild, 3 rows with `OriginBatchID`), `TestIngestBatchRollsBackOnDuplicate` (second file already exists → no new files, no rows, no rebuild), `TestIngestBatchToLaterStageRejected`, `TestUploadRejectedOnMirrorRepo`, `TestPromoteBatchMovesOnlyThatBatch` (two batches in repo A, promote batch 1 → only its files in B, rows in B keep `OriginBatchID`; then promote the same batch B→C works), `TestPromoteBatchEmptyIs400Error`.
- [ ] **Step 2–4:** run → FAIL, implement, run `go test ./internal/pipeline/` → PASS.
- [ ] **Step 5: Commit** `feat(pipeline): batch ingest and promote-by-batch`.

### Task 8: Sync engine

**Files:**
- Create: `internal/mirror/sync.go`, `internal/mirror/sync_test.go`

**Interfaces:**
- Consumes: Tasks 1–7 (`store.Mirror`, `Upstream`, `Plan`, `pipeline.Service.IngestBatch`, `secretbox.Box`, `store.CreateBatch/FinishBatch`).
- Produces:
  ```go
  type Syncer struct {
      Store *store.Store; Pipeline *pipeline.Service; Client *http.Client
      Box *secretbox.Box          // nil allowed when no mirror uses credentials
      StagingDir string; Concurrency int // default 4
      MaxRPMBytes int64; Timeout time.Duration // default 6h
  }
  type Result struct { Status string; BatchID int64; Added int } // Status: "synced" | "up to date"
  func (s *Syncer) Run(ctx context.Context, repoID int64, actor string) (Result, error) // requires the mirror already claimed (state running); always finishes the mirror row and the batch
  ```
  Flow per spec "Sync algorithm": decrypt creds → `Upstream.Packages` → `Plan` (filters from the mirror row, keep from `Store.RepoKeep`, existing from `ListPackages`) → download missing into `<StagingDir>/<repoID>/<sha256>` (write to `.part`, rename on verified completion; skip files already staged and verified) → only when all staged: `CreateBatch`, `IngestBatch`, `FinishBatch`, remove staged files. Nothing to download → status `up to date`, no batch. Errors are recorded via `FinishMirror(status "error", msg)` with the message scrubbed of the token and password. Next run time is computed by Task 9's `NextRun`; until then pass `nil`.

- [ ] **Step 1: Failing tests** (fake upstream from Task 5, `storetest`, `fakePub`-style publisher): `TestRunSyncsAndCreatesBatch`; `TestRunUpToDateCreatesNoBatch`; **`TestRunDownloadFailureLeavesRepoUnchangedAndKeepsStaged`** (third of three files fails → no rows, no files in repo dir, no rebuild; fix the upstream, rerun → the two staged files are not fetched again and the sync completes); **`TestRunChecksumMismatchPublishesNothing`**; `TestRunConflictingSameNameFileFails`; **`TestRunRespectsKeepNoRedownload`** (5 upstream versions, keep 3: first run stores 3, second run downloads 0); `TestRunDoesNotDeleteWhenUpstreamDropsPackage`; `TestRunWithCredentialsDecryptsAndSendsAuth`; `TestRunErrorNeverContainsToken` (token appears in neither `LastError` nor the returned error nor captured log output); `TestRunSignatureFailureFails`.
- [ ] **Step 2–4:** run → FAIL, implement (bounded worker pool with `errgroup`-style or plain goroutines; `golang.org/x/sync` is already an indirect dependency), run → PASS.
- [ ] **Step 5: Commit** `feat(mirror): staged, verified, atomic sync engine`.

### Task 9: Schedule and scheduler

**Files:**
- Create: `internal/mirror/schedule.go`, `internal/mirror/schedule_test.go`, `internal/mirror/scheduler.go`, `internal/mirror/scheduler_test.go`

**Interfaces:**
- Produces:
  ```go
  func ValidateSchedule(sc store.Schedule) error                       // frequency known; HH:MM valid; timezone loads (time.LoadLocation); weekly needs ≥1 weekday 0–6; monthly needs day 1–31
  func NextRun(sc store.Schedule, after time.Time) (*time.Time, error)  // nil for "manual"; strictly after `after`
  func (s *Syncer) Trigger(ctx context.Context, repoID int64, actor string) error // ClaimMirror (store.ErrBusy if running) then runs Run in a goroutine on context.WithoutCancel(ctx) with s.Timeout; computes and stores next run on finish
  func (s *Syncer) Start(ctx context.Context)                           // once: InterruptRunning; then every minute ClaimDueMirror loop; stops on ctx.Done()
  ```
  `Run`'s finish now stores `NextRun` from `NextRun(sc, time.Now())`. A schedule missed while the app was down runs once (a due `next_run` is claimed once; the next is computed from now).

- [ ] **Step 1: Failing tests (schedule):** `TestNextRunDaily`, `TestNextRunWeeklyPicksNextListedWeekday`, `TestNextRunMonthly`, **`TestNextRunMonthlyDay31InShortMonthUsesLastDay`**, **`TestNextRunNonexistentLocalTimeSpringForward`** (America/New_York 02:30 on the change day runs once, at the first valid instant after the gap), **`TestNextRunAmbiguousLocalTimeFallBackRunsOnce`**, `TestNextRunManualIsNil`, `TestValidateScheduleRejects` (bad tz, `25:00`, weekly with no days, monthly day 0/32).
- [ ] **Step 2: Failing tests (scheduler):** **`TestStartMarksInterruptedRunsStopped`** (row left `running` → idle + `interrupted` after `Start`), `TestDueMirrorRunsOnceNotPerMissedSlot`, **`TestTriggerWhileRunningReturnsErrBusy`**, `TestPausedMirrorNeverRuns`.
- [ ] **Step 3–4:** run → FAIL, implement with `time.Date` in the location (Go normalizes the gap forward; detect and handle the ambiguous hour by choosing the first occurrence), run → PASS.
- [ ] **Step 5: Commit** `feat(mirror): schedules and background scheduler`.

### Task 10: Faster rebuilds for big repos

**Files:**
- Modify: `internal/publish/publish.go`, `internal/pipeline/pipeline.go` (`rebuild` uses `Service.RebuildTimeout`)
- Test: `internal/publish/publish_test.go`, `internal/pipeline/batch_test.go`

**Interfaces:**
- Produces: `Publisher.Rebuild` passes `--update --update-md-path <repo>/repodata` to `createrepo_c` when `<repo>/repodata/repomd.xml` already exists; otherwise behaves as today. `Service.rebuild` uses `RebuildTimeout` (default 10 minutes when zero).

- [ ] **Step 1: Failing tests:** `TestRebuildUpdateProducesSamePackageSet` (build, add one rpm, rebuild; the second metadata lists both packages and `repomd.xml.asc` verifies), `TestRebuildAfterRemovingRpmDropsIt` (update mode must not resurrect a deleted file), pipeline `TestRebuildUsesConfiguredTimeout` (fake publisher records the context deadline).
- [ ] **Step 2–4:** run → FAIL, implement, run → PASS.
- [ ] **Step 5: Measure and record.** In a scratch directory copy the fixture rpm to 3000 distinct filenames, time a full `createrepo_c` and an `--update` run after adding one file. Expect the update run to be clearly faster. If it is not, keep the flag off and say so in the commit message. Put both timings in the commit message.
- [ ] **Step 6: Commit** `perf(publish): incremental createrepo_c rebuilds`.

### Task 11: API

**Files:**
- Create: `internal/api/mirror.go`, `internal/api/mirror_test.go`
- Modify: `internal/api/packages.go` (promote accepts `batch`; map `store.ErrMirrorRepo` → 409 in `pkgErr`), `internal/api/sources.go` (mirror repo → 409), `internal/api/repos.go` (`PUT .../previous` on a mirror → 409), `internal/api/router.go`, `internal/api/testenv_test.go`

**Interfaces:**
- Consumes: `store.Mirror`, `mirror.Syncer.Trigger`, `mirror.ParseRepoFile/ResolveVars/ResolveMetalink/ResolveMirrorList`, `mirror.ValidateSchedule/NextRun`, `secretbox.Box`.
- Produces: `type Mirrors struct { Store *store.Store; Syncer *mirror.Syncer; Box *secretbox.Box; Client *http.Client }` with `Routes(mux)`, and `Deps.Mirrors *Mirrors`.

| Route | Behavior |
|---|---|
| `POST /api/mirror/parse-repo` `{"repo_file":"..."}` | `200 {"sections":[{"id","name","url_kind":"baseurl|metalink|mirrorlist","variables":["releasever"],"has_credentials":bool}]}`; 400 if unparseable |
| `GET /api/repos/{slug}/mirror` | 200 config + `state,last_run,last_status,last_error,next_run,has_credentials`; never token or username secret material (`username` is returned, token never); 404 if not a mirror |
| `PUT /api/repos/{slug}/mirror` | body: `upstream_url` **or** `repo_file`+`section`+`vars`, `username`, `token`, `clear_credentials`, `gpg_key`, `require_signature`, `arches`, `include`, `exclude`, `keep_latest`, `schedule`. 200 saved; 409 `ErrNotEligible`; 400 invalid schedule, missing vars (`{"error":"...","missing_vars":[...]}`), several sections without `section` (`{"error":"...","sections":[ids]}`), credentials without `SECRET_KEY`, `require_signature` without a key. Omitting `token` keeps the stored one. Resolved URL (vars substituted, metalink/mirrorlist resolved) is what is stored |
| `DELETE /api/repos/{slug}/mirror` | 200; repo becomes a normal first stage; packages kept; 409 while syncing |
| `POST /api/repos/{slug}/mirror/sync` | 202 `{"status":"started"}`; 409 if running; 404 if not a mirror |
| `GET /api/repos/{slug}/batches` | 200 `[{id,started_at,finished_at,status,added,error}]` |
| `POST /api/repos/{slug}/promote` | accepts `{"batch":N}` in place of `filenames` (exactly one of the two, else 400); same result shape |

- [ ] **Step 1: Failing tests** (use `routerEnv`/`e.Do`): `TestParseRepoReportsSectionsAndVariables`; `TestPutMirrorRoundTripAndEligibility` (409 on a repo with packages); `TestPutMirrorMissingVarsListsThem`; `TestPutMirrorResolvesBaseurlVars`; **`TestMirrorResponsesNeverContainToken`** (token string absent from GET, PUT response and error bodies, and from the stored `token_enc` column bytes); `TestPutMirrorCredentialsNeedSecretKey`; `TestPutMirrorKeepsTokenWhenOmitted`; **`TestSyncWhileRunningIs409`**; `TestSyncNonMirrorIs404`; `TestUploadAndAddSourceOnMirrorAre409`; `TestPromoteBatchAndExclusiveBodyRule`; extend `adminRoutes` in `router_test.go` with every new route so anonymous = 401 and no-CSRF = 403.
- [ ] **Step 2–4:** run → FAIL, implement, run `go test ./internal/api/` → PASS.
- [ ] **Step 5: Commit** `feat(api): mirror configuration, sync, batches, promote by batch`.

### Task 12: Wiring

**Files:**
- Modify: `cmd/dnfrepo/main.go`

- [ ] **Step 1:** Build `secretbox` from `cfg.SecretKey` when set; set `pl.RebuildTimeout = cfg.RebuildTimeout`; create `mirror.Syncer{Store, Pipeline: pl, Box, StagingDir: filepath.Join(cfg.DataDir, "mirror-staging"), MaxRPMBytes: cfg.MaxUploadBytes}`; call `syncer.Start(ctx)` next to `sch.Start(ctx)`; pass `Mirrors` in `api.Deps`.
- [ ] **Step 2: Verify:** `go vet ./... && go build ./...`, then run the binary against the test Postgres with `BASE_URL`, `ADMIN_EMAIL`, `ADMIN_PASSWORD` set; `curl localhost:8080/healthz` → `ok`.
- [ ] **Step 3: End-to-end smoke** against a local fake upstream (a directory served with `python3 -m http.server`, built with `createrepo_c` from two fixture rpms): create repo, `PUT .../mirror` with the URL, `POST .../mirror/sync`, poll `GET .../mirror` until `last_status` is `synced`, then `dnf`-style fetch of `/r/<slug>/repodata/repomd.xml` returns 200 and `GET .../batches` shows one batch of 2.
- [ ] **Step 4: Commit** `feat: wire mirror syncer into the server`.

### Task 13: Web UI

**Files:**
- Modify: `web/src/api.ts`, `web/src/api.test.ts`, `web/src/pages/Repos.tsx`, `web/src/pages/RepoDetail.tsx`
- Create: `web/src/pages/MirrorPanel.tsx`, `web/src/schedule.ts`, `web/src/schedule.test.ts`

**Interfaces:**
- Produces in `api.ts`: types `Mirror`, `MirrorSchedule`, `Batch`, `RepoSection`; methods `parseRepo(text)`, `mirror(slug)`, `putMirror(slug, body)`, `deleteMirror(slug)`, `syncNow(slug)`, `batches(slug)`, and `promoteBatch(slug, batch)`. In `schedule.ts`: `describeSchedule(s: MirrorSchedule): string` (e.g. `Weekly on Tue, Thu at 02:00 America/New_York`, `Manual only`, `Paused`).
- UI: new-repo form gets *Type: Standard / Mirror* (Mirror creates the repo, then opens its page). `RepoDetail` for a mirror repo hides Upload and Sources, shows `MirrorPanel`: upstream (URL field **or** paste `.repo` → calls `parseRepo`, section picker, a prompt per missing variable), credentials (write-only, shows "stored" when `has_credentials`), GPG key + require-signature, filters (arches, include, exclude, keep latest), schedule (frequency, time, time zone defaulting to the browser's, weekdays, day of month, pause), **Sync now**, live status polled every 5 s while `state === 'running'`, and a *Batches* table with **Promote batch → next stage** (disabled when there is no next stage).

- [ ] **Step 1: Failing tests:** `schedule.test.ts` (the `describeSchedule` cases), `api.test.ts` (request method, path and body for `putMirror`, `syncNow`, `promoteBatch`; the `X-Requested-With` header is set).
- [ ] **Step 2–4:** run `npm test` → FAIL, implement, `npm test && npx tsc --noEmit && npm run build` → PASS.
- [ ] **Step 5: Manual check.** Run the server (Task 12 setup), open the repo page in a browser, create a mirror from pasted `.repo` text containing `$releasever`, confirm the variable prompt, save, press **Sync now**, watch the status go to synced, promote the batch. Report anything not checked.
- [ ] **Step 6: Commit** `feat(web): mirror repos UI`.

### Task 14: Documentation and help

**Files:**
- Modify: `web/src/pages/Help.tsx`, `README.md`, `docs/api.md`, `CLAUDE.md`, `docs/TODO.md`

- [ ] **Step 1: Help.** Add walkthroughs *Mirror a repo* and *Hub and spoke (dev → QA → prod sites)* (spoke = mirror of the hub's repo using a repo-user token and the hub's `/gpg.key`; mention verifying the signature and that a spoke signs with its own key, so clients use the spoke's `/gpg.key`), and definitions *Mirror*, *Sync*, *Batch*. Add the Fedora example (one mirror per release and repo, sync on the schedule you test against).
- [ ] **Step 2: README.** Mirror section (what it is, filters, schedule, first-sync behavior: nothing visible until the whole sync finishes, delete-by-hand comes back, disk use is the operator's concern, internal URLs allowed), env vars `SECRET_KEY` and `REBUILD_TIMEOUT` in the table.
- [ ] **Step 3: `docs/api.md`.** Document every route in Task 11 with request/response examples and error codes, including promote-by-batch.
- [ ] **Step 4: `CLAUDE.md`.** Add `mirror` to the layout line (parse `.repo`, read upstream, plan, schedule, sync) and one invariant: mirror syncs publish only through `pipeline.IngestBatch`, never write package files themselves; upstream credentials are sealed and never leave the API.
- [ ] **Step 5: `docs/TODO.md`.** Leave the snapshot item; add "dry-run/preview of a sync" and "mirror failover across metalink URLs" as deferred.
- [ ] **Step 6: Verify.** `go vet ./... && go test ./...` (with `DNFREPO_TEST_DSN`), `cd web && npm test && npx tsc --noEmit && npm run build`.
- [ ] **Step 7: Commit** `docs: mirror repos`.

---

## Self-review notes

- **Spec coverage:** goals 1–7 → Tasks 4/11/13 (input), 5/8 (upstreams, signature, credentials), 6 (filters), 9/11/13 (schedule, sync now, status), 2/7/11/13 (batches), 1/7 (first stage, promotion). Anti-goals: atomic publish → Tasks 7/8, no deletion → 6/8, credentials → 3/8/11, no blocking → 8/9 (detached goroutines). Calls: re-sign locally (existing `Rebuild`), encrypted creds (3), whole-sync atomicity (8), resolve vars at creation (4/11), empty repo only (1).
- **Deviation:** no `batch_packages` table (Task 2 note).
- **Open measurement:** Task 10 decides whether `--update` ships; the 10-minute rebuild cap is configurable regardless.
