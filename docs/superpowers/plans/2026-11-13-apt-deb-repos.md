# apt/deb repos Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** dnfrepo can create deb repos (own uploaded debs, or a mirror of a Debian/Ubuntu suite) next to rpm repos, served as a signed apt archive.

**Architecture:** `repos.kind` (`rpm`|`deb`) plus a `deb_repos` row (suite, components, arches). Deb code sits behind kind checks at the edges (pipeline, publisher, serve, API, mirror source). apt metadata is generated in pure Go (`aptindex`) from package rows and signed by the instance key (`Release.gpg` detached, `InRelease` clearsigned); `dists/` swaps atomically via generation dir + symlink. Package files stay flat in the repo dir; `pool/<component>/<file>` is a serve-time mapping.

**Tech Stack:** Go, PostgreSQL (pgx), ProtonMail go-crypto (`openpgp`, `openpgp/clearsign`), klauspost/compress + stdlib gzip, xi2/xz (already in go.mod), React/TS UI.

**Spec:** `docs/superpowers/specs/2026-11-13-apt-deb-repos-design.md`

## Global Constraints

- rpm repos, routes and tests behave byte-for-byte as before; every deb path is behind a kind check. Run the full existing suite after every task.
- No apt tooling in the image; no new dependency unless a task says why (none planned: write a small `ar` reader with stdlib).
- Migration `009_deb.sql` is the next number; never edit applied migrations.
- Only `pipeline` writes package files, under the per-repo mutex. Rebuilds run detached with `REBUILD_TIMEOUT`.
- Deb version strings are stored whole (epoch included) in `packages.version`; `release` is `''` for deb rows. Filenames are `<Package>_<version without epoch>_<Arch>.deb`.
- A repo's `suite` default is its slug; `components` default `["main"]`; `arches` default `["amd64","arm64"]`. At least one real arch (not `all`) is required.
- Promotion to a deb stage is rejected whole (`409`) if a package's component is not in the target's `components` or its arch is neither `all` nor in the target's `arches`.
- `Release` has no `Valid-Until`; `Date` is rebuild time. `Packages` always carries MD5sum, SHA1, SHA256.
- Plex sources are rpm-only (`409` on deb repos). GitHub sources pick `.deb` assets for deb repos.
- Tokens, the GPG private key and upstream credentials never appear in logs, responses or the DB readably.
- TDD: failing test first. Tests need Postgres: `podman run -d --rm --name dnfrepo-pg -p 5433:5432 -e POSTGRES_PASSWORD=pw postgres:17-alpine`, then `DNFREPO_TEST_DSN=postgres://postgres:pw@localhost:5433/postgres go test ./...`; stop it at the end. Work on branch `deb-repos`. Commit steps happen only after the user approves (project rule); use conventional messages.

## Review Focus

- **Empty deb repo** (no packages yet): `Release` and empty `Packages(.gz)` still valid and signed; apt must not error. Pinned in Task 5/6.
- **Epoch versions** (`1:2.0-1`): filename omits the epoch, `Packages` `Version:` keeps it, retention orders by epoch first. Task 1/4.
- **Hostile upstream `Filename:`** (`../../etc/x.deb`, absolute path, `//`): rejected, never fetched or written outside the pool. Task 10.
- **Oversized or bomb `Packages.xz/.gz`**: decompression capped; hash from `Release` checked before parsing. Task 10.
- **Unsigned or wrongly signed upstream** with `require_signature` / a key set: sync fails and the repo is unchanged. Task 10/11.

---

### Task 1: dpkg version comparison

**Files:** Create `internal/debver/debver.go`, `internal/debver/debver_test.go`

**Interfaces:** Produces `func Compare(a, b string) int` (-1/0/1) over full versions `[epoch:]upstream[-revision]`.

- [ ] **Step 1: Failing test `TestCompare`** (table): `1.0 < 1.1`; `1.0-1 < 1.0-2`; `1:1.0 > 2.0` (epoch); `1.0~rc1 < 1.0`; `1.0+b1 > 1.0`; `1.0a < 1.0.1`... use the dpkg rules: `~` sorts before everything including end; letters before non-letters; numeric runs compared as numbers; missing revision equals `0`; `2.0 == 2.0-0`. Also `TestCompareAntisymmetric` over the table pairs.
- [ ] **Step 2:** Run `go test ./internal/debver` → FAIL (undefined).
- [ ] **Step 3: Implement `Compare`** following Debian Policy §5.6.12 (split epoch at first `:`, revision at last `-`, compare each part with the alternating non-digit/digit algorithm where `~` < end < letters < other punctuation). Model structure on `internal/rpmver/rpmver.go`.
- [ ] **Step 4:** Run tests → PASS.
- [ ] **Step 5: Commit** `feat(debver): dpkg version comparison`.

### Task 2: Reading .deb files

**Files:** Create `internal/debmeta/debmeta.go`, `internal/debmeta/debmeta_test.go`, `internal/debmeta/debtest/debtest.go`

**Interfaces:**
- Produces:
  - `var ErrNotDeb = errors.New("not a valid deb file")`
  - `type Control struct{ Package, Version, Arch, Stanza string }` (`Stanza` = the control file verbatim, trailing newline trimmed).
  - `func Parse(path string) (Control, error)` (ar archive → `control.tar.{gz,xz,zst}` → `./control`; any failure is `ErrNotDeb`; cap the control file at 1 MiB).
  - `func Filename(c Control) string` → `<Package>_<Version minus "N:">_<Arch>.deb`.
  - `type Stanza struct{ Fields map[string]string; Raw string }` and `func ParseStanzas(r io.Reader) ([]Stanza, error)` (blank-line separated, continuation lines kept in `Raw` and joined with `\n` in `Fields`; reject any line longer than 1 MiB).
  - `debtest.Build(t testing.TB, s debtest.Spec) []byte` with `Spec{Package, Version, Arch string; Fields map[string]string; Compress string}` (`""`/`"gz"`, `"xz"` via the xz lib is not writable, so tests use `gz` and `zst`); writes a valid ar with `debian-binary`, `control.tar.<c>`, `data.tar.gz`.
- Consumes: nothing.

- [ ] **Step 1: Failing tests:** `TestParseGz` and `TestParseZst` (Package/Version/Arch/Stanza contain `Depends: libc6` from `Fields`), `TestFilenameStripsEpoch` (`1:2.0-1`, amd64 → `hello_2.0-1_amd64.deb`), `TestParseRejectsGarbage` (text file and truncated ar → `ErrNotDeb`), `TestParseStanzas` (two stanzas, continuation line, Raw preserved).
- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3: Implement** `debmeta.go` (hand-rolled `ar` reader: 8-byte magic, 60-byte headers, even padding; compression by member suffix using stdlib gzip, `xi2/xz`, klauspost `zstd`) and `debtest.go`.
- [ ] **Step 4:** Run → PASS.
- [ ] **Step 5: Commit** `feat(debmeta): read .deb control data`.

### Task 3: Store: kind, deb repo settings, package fields

**Files:** Create `internal/store/migrations/009_deb.sql`, `internal/store/deb.go`, `internal/store/deb_test.go`. Modify `internal/store/repos.go`, `internal/store/packages.go`, `internal/store/batches.go` (every `INSERT INTO packages` and `pkgCols`/`scanPkg`/`repoCols`/`scanRepo`).

**Interfaces:**
- Produces:
  - `const KindRPM, KindDeb = "rpm", "deb"`; `Repo.Kind string`.
  - `Package` gains `Component, Control, MD5, SHA1 string` (all `''` for rpm).
  - `type DebRepo struct{ RepoID int64; Suite string; Components, Arches []string }`
  - `func (s *Store) CreateDebRepo(ctx, slug, name string, mode AccessMode, previousID *int64, d DebRepo) (Repo, error)` (one transaction; sets `repos.kind='deb'`; `d.Suite` empty → slug; validates suite with `ValidSuite`, ≥1 component, ≥1 real arch; `ErrBadDeb` otherwise).
  - `func (s *Store) DebRepoByID(ctx, repoID int64) (DebRepo, error)` (`ErrNotFound` for rpm repos).
  - `func (s *Store) SetDebRepo(ctx, repoID int64, d DebRepo) error` (replaces suite/components/arches; same validation).
  - `func ValidSuite(s string) bool` (`^[a-z0-9][a-z0-9.-]{0,62}$`), `var ErrBadDeb`.
- Migration `009`: `repos.kind text NOT NULL DEFAULT 'rpm' CHECK (kind IN ('rpm','deb'))`; table `deb_repos(repo_id PK → repos ON DELETE CASCADE, suite text NOT NULL, components text[] NOT NULL, arches text[] NOT NULL)`; `packages` gains `component, control, md5, sha1` as `text NOT NULL DEFAULT ''`.

- [ ] **Step 1: Failing tests (`deb_test.go`, using `storetest.New`):** `TestCreateDebRepoDefaults` (suite=slug, kind deb), `TestCreateDebRepoRejectsAllOnlyArch` and bad suite → `ErrBadDeb`, `TestDebRepoByIDNotFoundForRPM`, `TestPackageDebFieldsRoundTrip` (insert via `AddPackage` with the four fields, `ListPackages` returns them; same through `AddPackagesAudited`), `TestExistingRepoIsRPM` (CreateRepo → Kind rpm).
- [ ] **Step 2:** Run `go test ./internal/store` → FAIL.
- [ ] **Step 3:** Write the migration; extend the structs and every insert/select listed above so the four deb fields round-trip; implement `deb.go`.
- [ ] **Step 4:** Run `go test ./...` → all PASS (rpm tests unchanged).
- [ ] **Step 5: Commit** `feat(store): deb repo kind, settings and package fields`.

### Task 4: Pipeline: upload, batch ingest, promote, retention for deb

**Files:** Modify `internal/pipeline/pipeline.go`, `internal/pipeline/batch.go`, `internal/pipeline/promote.go`, `internal/pipeline/keep.go`. Create `internal/pipeline/deb.go`, `internal/pipeline/deb_test.go`.

**Interfaces:**
- Consumes: `debmeta.Parse/Filename`, `debver.Compare`, `store.DebRepoByID`, `store.Package` deb fields.
- Produces:
  - `func ValidDebFilename(name string) bool` (`^[A-Za-z0-9][A-Za-z0-9.+~_-]*\.deb$`).
  - `var ErrWrongKind` (file extension does not match the repo kind, HTTP 400), `ErrBadArch` (arch not in repo's list and not `all`, 400), `ErrFilenameMismatch` (filename ≠ `debmeta.Filename`, 400), `ErrDebIncompatible` (409).
  - `StagedFile` gains `Component, Control string` (empty for rpm; for deb the mirror supplies the upstream stanza and component).
  - `func comparePackage(kind, verA, relA, verB, relB string) int` (rpmver for rpm, `debver.Compare(verA, verB)` for deb) used by `purgeLocked`.

- [ ] **Step 1: Failing tests (`deb_test.go`; reuse `newSvc`/`mkRepo`, add a `mkDebRepo(t, s, fp, slug, prev *int64, d store.DebRepo)` helper using `Store.CreateDebRepo`):**
  - `TestUploadDebFillsRow`: upload `debtest.Build` bytes named by `debmeta.Filename`; row has Name, full Version, Arch, Component `main`, non-empty MD5/SHA1/SHA256/Control, `Release==""`; file at repo dir; one rebuild.
  - `TestUploadDebWrongExtensionAndKind`: `.rpm` to a deb repo and `.deb` to an rpm repo → `ErrWrongKind`.
  - `TestUploadDebFilenameMismatch`, `TestUploadDebArchNotInRepo` (arch `s390x`), `TestUploadDebArchAllAccepted`.
  - `TestUploadDebEpochFilename` (version `1:2.0-1`, filename without `1:`, row Version `1:2.0-1`).
  - `TestPromoteDebRejectsIncompatibleStage` (source component `universe`, target only `main` → `ErrDebIncompatible`, target unchanged) and `TestPromoteDebCarriesFields` (all four deb fields present on the promoted row).
  - `TestRetentionDebOrdersByEpoch` (keep 1; `1:1.0-1` survives over `2.0-1`).
  - `TestIngestBatchDeb` (StagedFile with Component/Control stored verbatim).
- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3: Implement.** Kind dispatch at the top of `Upload`/`IngestBatch`/`Promote` (`repo.Kind`); for deb uploads hash with sha256+md5+sha1 in the existing `MultiWriter`, parse with `debmeta`, component = `Components[0]`; `Promote` validates every selected package against the target's `DebRepo` before touching files and copies `Component, Control, MD5, SHA1` into the new rows; `QuarantineOrphans` treats `.deb` names as package files in deb repos; `purgeLocked` calls `comparePackage(repo.Kind, ...)`.
- [ ] **Step 4:** Run `go test ./internal/pipeline ./internal/store` → PASS, then `go test ./...` → PASS.
- [ ] **Step 5: Commit** `feat(pipeline): deb upload, ingest, promotion and retention`.

### Task 5: apt index generation and clearsign

**Files:** Create `internal/aptindex/aptindex.go`, `internal/aptindex/aptindex_test.go`. Modify `internal/signer/signer.go` (+ `signer_test.go`).

**Interfaces:**
- Produces:
  - `type Package struct{ Component, Arch, Filename string; Size int64; MD5, SHA1, SHA256, Control string }`
  - `func Build(suite string, components, arches []string, pkgs []Package, now time.Time) map[string][]byte` — keys relative to `dists/<suite>/`: `Release`, and per component/arch `<c>/binary-<a>/Packages`, `Packages.gz`, `Release`. A package of arch `all` appears in every arch's `Packages`; others only in their arch; a package whose arch is not in `arches` is omitted. Each stanza is `Control` with `Filename`, `Size`, `MD5sum`, `SHA1`, `SHA256` set (any such lines already in `Control` removed first), `Filename` = `pool/<component>/<Filename>`, stanzas sorted by Package then Version, blank-line separated. `Release` fields: Origin, Label, Suite, Codename (= suite), Date (RFC1123Z UTC), Architectures, Components, Description, then `MD5Sum:`, `SHA1:`, `SHA256:` blocks listing every other file with size and hash.
  - `func (s *Signer) ClearSign(data []byte) ([]byte, error)` (`openpgp/clearsign`).

- [ ] **Step 1: Failing tests:** `TestBuildEmptyRepo` (no packages → `Packages` empty, `Packages.gz` decompresses to empty, `Release` lists both files with correct hashes); `TestBuildAllFanOut` (`all` package in both `binary-amd64` and `binary-arm64`, `amd64` package only in amd64); `TestBuildStanzaFields` (Filename `pool/main/x.deb`, Size, MD5sum/SHA1/SHA256 correct, no duplicated `Size:` when Control already had one); `TestReleaseHashesMatchFiles` (recompute each listed hash from the map); `TestClearSignVerifies` (sign with a `LoadOrCreate` key in a temp dir, verify with `clearsign.Decode` + `openpgp.CheckDetachedSignature` against the public key).
- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3: Implement** both. Gzip with `compress/gzip` at a fixed mtime so output is deterministic.
- [ ] **Step 4:** Run → PASS.
- [ ] **Step 5: Commit** `feat(aptindex): apt Packages/Release generation and clearsign`.

### Task 6: Publisher: deb rebuild with atomic dists swap

**Files:** Modify `internal/publish/publish.go`, `cmd/dnfrepo/main.go` (startup repodata check and `pub.Store`). Create `internal/publish/apt.go`, `internal/publish/apt_test.go`.

**Interfaces:**
- Consumes: `aptindex.Build`, `Signer.SignDetached/ClearSign`, `store.DebRepoByID`, `store.ListPackages`.
- Produces: `Publisher.Store *store.Store` (nil keeps old behaviour); `Rebuild` dispatches to `rebuildApt(ctx, slug)` when the repo's kind is deb. Layout: `<repo>/dists.<gen>/<suite>/...` written fully, `Release.gpg` and `InRelease` last, then symlink `dists` → `dists.<gen>` swapped by rename; keep the two newest generations (`pruneGenerations(dir, prefix string)` generalised, rpm call passes `"repodata."`).

- [ ] **Step 1: Failing tests (real signer in temp dir, `storetest.New`):** `TestAptRebuildWritesSignedArchive` (create deb repo, add two package rows, files exist for sizes; after `Rebuild`: `dists/<suite>/Release`, `Release.gpg`, `InRelease` exist; detached signature and clearsign both verify with `PublicKeyArmored`; `InRelease` body equals `Release`); `TestAptRebuildEmptyRepo` (no packages still produces all three signed files); `TestAptRebuildSwapsAtomically` (second rebuild changes the `dists` symlink target, old generation kept, third prunes the first); `TestRPMRebuildUnchanged` is the existing publish tests.
- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3: Implement** `apt.go`; in `main.go` set `pub.Store = st` and make the startup "repodata missing → rebuild" check look for `dists` on deb repos.
- [ ] **Step 4:** Run `go test ./internal/publish ./...` → PASS.
- [ ] **Step 5: Commit** `feat(publish): signed apt archive generation`.

### Task 7: Serving deb repos

**Files:** Modify `internal/serve/serve.go`, `internal/serve/browse.go`, `internal/serve/repofile.go`. Create `internal/serve/apt.go`, `internal/serve/apt_test.go`.

**Interfaces:** deb repos serve only `dists/<suite>/<...>` (each path segment matches `^[A-Za-z0-9][A-Za-z0-9._+~-]*$`, resolved through the `dists` symlink, regular files only) and `pool/<component>/<file>.deb` mapped to the flat `<repo>/<file>.deb` (component must be in the repo's `components`). `<slug>.repo` returns 404 for deb repos. The browse page for a deb repo shows the `deb [signed-by=/etc/apt/keyrings/dnfrepo.asc] <BaseURL>/r/<slug>/ <suite> <components>` line, the key setup (`curl -fsS <BaseURL>/gpg.key | sudo tee /etc/apt/keyrings/dnfrepo.asc`), and the package list. rpm routing untouched.

- [ ] **Step 1: Failing tests (build a repo dir by hand + fake store as existing serve tests do):** `TestServesDists` (Release, InRelease, `main/binary-amd64/Packages.gz` → 200); `TestServesPoolMapsToFlatFile` (200 with the file bytes; `pool/universe/x.deb` for an unknown component → 404); `TestAptTraversalRejected` (`dists/../x`, `pool/main/../y.deb`, encoded variants → 404); `TestRepoFile404ForDeb`; `TestBrowseDebShowsSourcesLine`; `TestAccessGuardCoversDeb` (ips-mode repo denies a foreign IP on `dists/`).
- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3: Implement** kind dispatch in `dispatch`/`file` and the deb browse template.
- [ ] **Step 4:** Run `go test ./internal/serve` → PASS.
- [ ] **Step 5: Commit** `feat(serve): serve deb repos`.

### Task 8: API: create, update, upload errors, sources gating

**Files:** Modify `internal/api/repos.go`, `internal/api/packages.go` (`pkgErr`), `internal/api/sources.go`. Create `internal/api/deb_test.go`.

**Interfaces:** `POST /api/repos` accepts optional `kind` (`rpm` default | `deb`), and for deb `suite`, `components`, `arches` (defaults per Global Constraints; bad values `400`). `repoJSON` gains `kind` and, for deb, `suite`, `components`, `arches`. `PATCH /api/repos/{slug}` accepts `arches` for deb repos (rebuilds via `OnChange`); `409` for rpm repos. `pkgErr`: `ErrWrongKind`, `ErrBadArch`, `ErrFilenameMismatch` → 400; `ErrDebIncompatible` → 409. `POST /api/repos/{slug}/sources` with kind `plex` on a deb repo → 409; `github` allowed. `pkgJSON` gains `component` (omitted when empty).

- [ ] **Step 1: Failing tests:** `TestCreateDebRepoViaAPI` (201, JSON has kind/suite/components/arches and `repos/<slug>` dir exists), `TestCreateDebRepoBadSuite400`, `TestListShowsKind`, `TestPatchDebArches` and `TestPatchArchesOnRPM409`, `TestUploadDebViaAPI` (201) and wrong-kind upload 400, `TestPromoteDebIncompatible409`, `TestPlexSourceOnDeb409`.
- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3: Implement** the handlers and error mapping.
- [ ] **Step 4:** Run `go test ./internal/api` → PASS.
- [ ] **Step 5: Commit** `feat(api): deb repo creation, update and error mapping`.

### Task 9: GitHub sources for deb repos

**Files:** Modify `internal/sources/github.go`, `internal/sources/scheduler.go`. Add tests to `internal/sources/sources_test.go`/`scheduler_test.go`.

**Interfaces:** Produces `func LatestGitHubDeb(ctx context.Context, c *http.Client, apiBase, token, ownerName string, arches []string) (Release, error)`: latest release's `.deb` assets; pick the one whose `_<arch>.deb` suffix matches the first of `arches` that has an asset, else an `_all.deb`; none → error. `Scheduler.latest` uses it when the repo kind is deb (arches from `DebRepoByID`); the filename is the asset name unchanged (no `VersionedName` rewrite), so a name that disagrees with the control data fails upload with `ErrFilenameMismatch` and is recorded as the run error.

- [ ] **Step 1: Failing tests (reuse `ghServer`):** `TestLatestGitHubDebPicksAMD64`, `TestLatestGitHubDebFallsBackToAll`, `TestLatestGitHubDebNoDebErrors`, `TestSchedulerFetchesDebIntoDebRepo` (end to end with a `debtest` asset into a deb repo; second run `up to date`), `TestSchedulerDebNameMismatchRecordsError`.
- [ ] **Step 2:** Run → FAIL. **Step 3:** Implement. **Step 4:** Run `go test ./internal/sources` → PASS.
- [ ] **Step 5: Commit** `feat(sources): GitHub .deb assets for deb repos`.

### Task 10: apt upstream reader

**Files:** Create `internal/mirror/aptupstream.go`, `internal/mirror/aptupstream_test.go`. Modify `internal/mirror/upstream.go` (`UpstreamPkg`), `internal/mirror/fakeupstream_test.go` (add a fake apt archive builder).

**Interfaces:**
- `UpstreamPkg` gains `Component, Control string` (deb only); for deb, `Version` is the full version, `Release` empty, `Filename` = `debmeta`-style name taken from the pool path's base, `Href` = validated relative pool path.
- Produces:
  - `type AptUpstream struct{ Base, Suite string; Components, Arches []string; Client *http.Client; Username, Token, GPGKey string; RequireSig bool; MaxDebBytes int64 }`
  - `func (u *AptUpstream) Packages(ctx context.Context) ([]UpstreamPkg, error)` and `func (u *AptUpstream) Download(ctx context.Context, p UpstreamPkg, dst io.Writer) error` (SHA256 and size verified; same shape as `Upstream`).
  - `type packageSource interface{ Packages(context.Context) ([]UpstreamPkg, error); Download(context.Context, UpstreamPkg, io.Writer) error }` satisfied by `*Upstream` and `*AptUpstream`.
- Behaviour: fetch `dists/<suite>/InRelease`, fall back to `Release` + `Release.gpg`. If `GPGKey` set, require a valid signature from at least one key in it (else error); `RequireSig` without a key is an error. For each wanted component/arch (plus `all`), take `Packages.xz` else `.gz` as listed in `Release`, verify its SHA256 from `Release` **before parsing**, cap decompressed size (1 GiB, dictionary cap like `maxDict`), parse with `debmeta.ParseStanzas`. Keep stanzas whose `Architecture` is a wanted arch or `all`. Reject a stanza whose `Filename` is absolute, contains `..`, `//`, or a backslash, or whose base name fails `pipeline.ValidDebFilename`, or whose `SHA256` is not 64 hex or `Size` ≤ 0.

- [ ] **Step 1: Failing tests (httptest fake archive signed with a test key):** `TestAptPackagesParsesAndFilters` (two components, two arches, `all` kept); `TestAptVerifiesInReleaseSignature` and `TestAptUnsignedWithKeyFails`, `TestAptWrongKeyFails`, `TestAptRequireSigWithoutKeyErrors`; `TestAptPackagesHashMismatchFails` (tampered `Packages.gz`); `TestAptHostileFilenameRejected` (table: `../../x.deb`, `/abs/x.deb`, `pool//x.deb`, `pool/main/a\b.deb`); `TestAptDecompressionCapped` (a gzip bomb over the cap errors); `TestAptDownloadVerifies` (wrong body → `ErrChecksum`); `TestAptSendsBasicAuth`.
- [ ] **Step 2:** Run → FAIL. **Step 3:** Implement. **Step 4:** Run `go test ./internal/mirror` → PASS.
- [ ] **Step 5: Commit** `feat(mirror): apt upstream reader`.

### Task 11: Deb mirror sync

**Files:** Modify `internal/mirror/sync.go`, `internal/mirror/plan.go`, `internal/pipeline/batch.go` (already deb-aware from Task 4). Add tests to `internal/mirror/sync_test.go`, `plan_test.go`.

**Interfaces:** `Syncer.run` builds an `AptUpstream` (Suite/Components/Arches from `Store.DebRepoByID`, credentials and key from the mirror row) when `repo.Kind == store.KindDeb`, else the existing `Upstream`; `stageAll`/`stage` take `packageSource`. `stageAll` copies `Component`/`Control` into each `pipeline.StagedFile`. `checkHeaders` for deb compares `debmeta.Parse` (Package, Version, Arch) to the upstream entry. `Filter` gains `Compare func(verA, relA, verB, relB string) int` (nil → `rpmver.ComparePackage`); deb passes `func(a, _, b, _ string) int { return debver.Compare(a, b) }`; `Plan` treats arch `all` like `noarch` in the arch filter.

- [ ] **Step 1: Failing tests:** `TestPlanDebKeepLatestUsesDpkgOrder` (epoch wins), `TestPlanAllArchAlwaysAllowed`, `TestSyncDebMirrorEndToEnd` (fake signed archive, small debs from `debtest`; after `Run`: rows with component/control, one batch, `dists` published and signed, files present; second `Run` → `up to date`), `TestSyncDebBadUpstreamSignatureLeavesRepoUnchanged` (no rows, no files, no `dists` change), `TestSyncDebHeaderMismatchFails`.
- [ ] **Step 2:** Run → FAIL. **Step 3:** Implement. **Step 4:** Run `go test ./internal/mirror ./...` → PASS.
- [ ] **Step 5: Commit** `feat(mirror): sync apt suites`.

### Task 12: Mirror API for deb repos

**Files:** Modify `internal/api/mirror.go`. Create tests in `internal/api/deb_mirror_test.go`.

**Interfaces:** `PUT /api/repos/{slug}/mirror` on a deb repo takes `upstream_url`, `suite`, `components`, `arches`, plus the existing `username`, `token`, `gpg_key`, `require_signature`, `include`, `exclude`, `keep_latest`, `schedule`; `.repo`-text input (`repo_text`/section fields) is `400` for deb. It writes suite/components/arches with `Store.SetDebRepo` (same eligibility rule: empty first-stage repo) and `mirrors.arches` from `arches`. GET returns `suite`, `components`, `arches` for deb mirrors. `POST /api/mirror/parse-repo` stays rpm-only. `gpg_key` must be an ASCII-armored public key (existing check), `require_signature` needs a key.

- [ ] **Step 1: Failing tests:** `TestPutDebMirror` (200, GET shows suite/components/arches, repo's `deb_repos` updated), `TestPutDebMirrorRepoTextIs400`, `TestPutDebMirrorNeedsSuite400`, `TestPutDebMirrorOnRepoWithPackages409`, `TestDebMirrorRejectsUploads409` (existing `ErrMirrorRepo` path), `TestSyncNowDebMirror202`.
- [ ] **Step 2:** Run → FAIL. **Step 3:** Implement. **Step 4:** Run `go test ./internal/api` → PASS.
- [ ] **Step 5: Commit** `feat(api): deb mirror configuration`.

### Task 13: Web UI

**Files:** Modify `web/src/api.ts` (+ `api.test.ts`), `web/src/pages/Repos.tsx`, `web/src/pages/RepoDetail.tsx`, `web/src/pages/MirrorPanel.tsx`, `web/src/styles.css` if needed.

**Interfaces:** `Repo` type gains `kind: 'rpm' | 'deb'`, `suite?`, `components?`, `arches?`; `createRepo` sends `kind` and deb fields; mirror save sends `suite/components/arches` for deb. A helper `sourcesListLine(repo, origin): string` in `api.ts` returns the apt line from Task 7 (unit-tested).

- [ ] **Step 1: Failing tests (`api.test.ts`):** `sourcesListLine` output exact for one repo; `createRepo` posts `kind:'deb'` with suite/components/arches.
- [ ] **Step 2:** `cd web && npm test` → FAIL.
- [ ] **Step 3: Implement UI:** new-repo form gets Kind (rpm/deb) and, for deb, suite, components, arches inputs; repo page shows the sources.list line and key commands instead of the `.repo` link, upload `accept=".deb"`, packages table shows component; Mirror panel shows suite/components/arches fields (no `.repo` paste) for deb; Plex source option hidden for deb repos.
- [ ] **Step 4:** Run `cd web && npm test && npx tsc --noEmit && npm run build` → all pass.
- [ ] **Step 5: Commit** `feat(web): deb repos`.

### Task 14: Docs, project notes, full verification

**Files:** Modify `README.md`, `docs/api.md`, `web/src/pages/Help.tsx`, `CLAUDE.md` (layout line gains `debver`, `debmeta`, `aptindex`; invariant note that deb code is behind kind checks), `docs/TODO.md` if it lists deb.

- [ ] **Step 1:** Update README (Features bullet, deb repo section: create, upload, mirror, client `sources.list` + key setup), `docs/api.md` (kind/suite/components/arches fields, deb mirror body, error codes), Help page (definitions: Suite, Component; walkthroughs "Create a deb repo and install from it", "Mirror Debian or Ubuntu").
- [ ] **Step 2: Full verification:** start Postgres; `go vet ./...`; `DNFREPO_TEST_DSN=... go test ./...`; `cd web && npm test && npx tsc --noEmit && npm run build`; stop Postgres. Expected: all pass, no change in rpm test results.
- [ ] **Step 3: Manual check (if `apt` available, e.g. in a Debian container):** run the server with a temp data dir, upload a deb, `apt-get update` against `http://host/r/<slug>/` using the key; expected: no signature or hash errors.
- [ ] **Step 4: Commit** `docs: apt/deb repos`.
