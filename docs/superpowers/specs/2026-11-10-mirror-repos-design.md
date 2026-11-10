# Mirror repos: sync a repo from an upstream

Status: draft for approval. Date: 2026-11-10.

## Intent

The author distributes packages hub-and-spoke:

- **Dev** is the hub: packages land in dev and are promoted to QA.
- **QA** may be on another continent, so a second dnfrepo runs there and syncs only the QA repo from the hub.
- **Prod** has about 100 sites. Each runs a dnfrepo with a local copy of prod, so clients use the local backbone instead of the WAN.
- **Fedora cache:** a local copy of Fedora's main repo to save internet bandwidth, synced only on chosen days. Patches then reach dev and QA first and production only after testing.

Today dnfrepo can only receive packages by upload, by the GitHub/Plex sources, or by promotion. A **mirror repo** is a repo whose packages come from syncing an upstream repo on a schedule. Each site runs its own dnfrepo, so the "users managing the repo" are that site's existing admins.

## Goals

1. Create a mirror repo from an upstream **URL (baseurl)** or by **pasting a `.repo` file**.
2. Upstream can be any dnf repo: Fedora, another dnfrepo (including `users`-mode with a repo-user token), anything with `repodata/repomd.xml`.
3. **Filters** applied at sync time, so excluded packages are never downloaded: architectures, name include and exclude patterns, latest N versions per (name, arch).
4. **Schedule** per mirror: manual only, daily, weekly (chosen weekdays) or monthly (day of month), at a time of day in a chosen time zone, plus pause. Any admin can edit it.
5. **Sync now**, running in the background with visible status (state, last run, counts, error).
6. **Batches:** every sync that changes the repo is recorded as a batch, the list of rpms it added. Any admin can promote a whole batch to the next stage in one action. The batch identity follows packages through promotion, so "the Nov 5 packages" can be promoted from QA to prod while newer syncs sit in dev.
7. A mirror is the first stage of a pipeline and can be promoted from like any first stage (Fedora mirror → dev → QA → prod).

## Non-goals (first version)

- **Frozen point-in-time repo copies with their own URL.** Deferred, see `docs/TODO.md`.
- Mirroring modular/AppStream metadata, delta rpms, source rpms, or non-rpm content.
- Verifying per-package rpm GPG signatures (consistent with the project: only metadata is signed).
- Mirroring upstream deletions: a sync never deletes a package the repo already has.
- A dry-run or "preview what would be downloaded" screen.
- Per-repo roles or permissions.
- Bandwidth limiting, proxies, resumable-across-restart partial downloads beyond the staging cache below.

## Anti-goals (a failure even if it technically works)

- Clients ever seeing a half-synced repo: new rpms visible without matching metadata, or metadata that references files that are not there. A sync publishes once, atomically, through the existing metadata swap.
- A package entering a non-first stage other than by promotion (a mirror is a first stage; no sync may write into a later stage).
- A sync silently deleting something that was already tested and promoted.
- Upstream credentials (repo-user tokens) appearing in logs, API responses, or the database in readable form.
- A running or stuck sync blocking repo serving or other repos' operations.
- A package accepted whose checksum does not match the upstream metadata.

## Constraints

- Go backend, PostgreSQL, existing `pipeline` as the ONLY writer of package files, under the per-repo mutex. Add numbered migrations; never edit applied ones.
- Existing invariants in `CLAUDE.md` stay true. Sync runs detached from request contexts, like rebuild.
- Existing facts the builder must work with:
  - `pipeline.Upload` rebuilds and signs after every file. Sync needs a batch ingest path with one purge and one rebuild per sync.
  - `Rebuild` runs full `createrepo_c` under a 10-minute timeout. A Fedora-sized repo may exceed that; the builder must measure and use `createrepo_c --update` with a persistent cache and/or a longer timeout for mirrors.
  - Retention (`keep`) purges old versions. If sync re-downloaded them, the repo would loop. Sync must apply the keep limits when planning.
- Tests: TDD, with a fake upstream served by `httptest` (repomd + primary + rpms). No real network in tests.

## Decisions by the author

- Mirror upstream deletions: **no**. Keep a package until retention purges it.
- Managers are the site's existing admins. No per-repo roles.
- Schedule as described in Goal 4.
- Batch promotion in v1; frozen snapshots later.
- Filters are wanted.

## Design

### Data model (calls by the builder within these shapes)

- `mirrors`, one row per mirror repo (unique `repo_id`): `upstream_url`, optional upstream credentials (username + encrypted token), `gpg_key_url`/`gpg_key` for verifying metadata, `arches text[]`, `include text[]`, `exclude text[]`, `keep_latest int` (0 = all), schedule fields (`frequency`, `time_of_day`, `timezone`, `weekdays`, `day_of_month`, `paused`), run state (`state`, `last_run`, `last_status`, `last_error`, `next_run`).
- `sync_batches` (`id`, `repo_id`, `started_at`, `finished_at`, `status`, `added_count`, `error`) and `batch_packages` (`batch_id`, `filename`).
- A nullable `origin_batch_id` on package rows. Promotion copies it to the new row. "Promote batch N" in any stage means all packages in that repo with `origin_batch_id = N`.

### Creating a mirror

- Only on an **empty, first-stage** repo that has no sources (mirrors the existing `ErrHasContent` rule). A mirror repo rejects uploads and added sources, and cannot be linked behind another repo.
- Input is a baseurl, or `.repo` text. From `.repo` text the app reads `baseurl`, `metalink`/`mirrorlist` (resolved to the first usable URL), `gpgkey`, `repo_gpgcheck`, and `username`/`password`. If several `[sections]` are present the user picks one in the UI. Section options the app doesn't understand are ignored.
- Removing the mirror config turns the repo into a normal first-stage repo with its packages kept.

### Sync algorithm

1. Fetch `repomd.xml`. If a GPG key is configured (or `repo_gpgcheck=1`), fetch `repomd.xml.asc` and verify it; fail the sync if it doesn't verify.
2. Fetch and verify the `primary` metadata (`.xml.gz`/`.zst`/`.xz`) against its checksum in `repomd.xml`; list packages with name, arch, version, release, checksum, size, location.
3. Apply filters and the repo's keep limits to get the wanted set; subtract what the repo already has (same filename and checksum). A same-name file with a different checksum fails the sync loudly.
4. Download each missing rpm into a **staging directory outside the repo**, verifying size and checksum as it streams. Concurrency is small and fixed. A completed staged file survives a failed sync so the retry skips it.
5. **Only if every file is staged and verified**, take the repo mutex and, in one step: place files, insert package rows and the batch record, apply retention, rebuild once. If any download fails, nothing is published and the repo is unchanged.
6. A sync with nothing new records "up to date" and creates no batch.

### Scheduler

- A single loop checks due mirrors each minute. A mirror that is due, not paused and not already syncing is started in the background with its own context and timeout (not a request's). One sync at a time per mirror; **Sync now** while one is running returns 409.
- A schedule missed because the app was down runs once at startup, not once per missed slot.
- State is persisted, so a restart mid-sync shows the run as failed ("interrupted") instead of stuck "running".
- Existing `sources` (GitHub/Plex) are unchanged.

### API (all admin, session or API token; documented in `docs/api.md`)

- `GET/PUT/DELETE /api/repos/{slug}/mirror`: read, create/replace, or remove the mirror config. `PUT` accepts either `upstream_url` or `repo_file` text. Credentials are write-only and never returned.
- `POST /api/repos/{slug}/mirror/sync`: start a sync now. `202` with the run state, `409` if running.
- `GET /api/repos/{slug}/batches`: batches with counts.
- `POST /api/repos/{slug}/promote` gains `{"batch": <id>}` as an alternative to `filenames`; it resolves to that batch's packages in this repo and goes through the existing all-or-nothing promote.

### UI

- New repo form: a type choice (standard or mirror). A mirror repo's page shows an upstream section (URL or paste `.repo`), filters, schedule, **Sync now** with status, and a Batches list with *Promote batch → next stage*.
- Help page gets a "Mirror a repo" and a "Hub and spoke" walkthrough; definitions for mirror, batch, sync.

### Audit and actors

Audit actor `mirror:<slug>` for sync writes; events `synced` (with counts) and `sync-failed`. Promotion of a batch is attributed to the admin or token.

## Calls made by the author of this document (not decided by the user; veto freely)

- Re-sign locally: a spoke verifies the upstream signature and then rebuilds and signs its own metadata with its own key. Spoke clients trust the spoke's `/gpg.key`.
- Upstream credentials are stored **encrypted** (AES-GCM) with a key from a new env var `SECRET_KEY`. It is required only when a mirror uses credentials. Plaintext in the DB is not acceptable under the existing invariant.
- Whole-sync atomicity (all files or none) instead of partial publish, accepting a long first sync before anything appears.
- A manually deleted mirrored package comes back at the next sync unless a filter excludes it.
- Metalink/mirrorlist resolve to the first usable URL; no mirror failover in v1.
- An admin may point a mirror at any URL, including internal addresses. Admins are trusted.
- A mirror can only be created on an empty repo.
- `.repo` variables: when pasted `.repo` text or a URL contains `$releasever` or `$basearch`, the form asks for a value for each (for example `42` and `x86_64`) and stores the resolved URL. No variable is evaluated at sync time. A Fedora cache therefore needs one mirror per release and per repo (`fedora`, `updates`).
- No disk quota; the staging directory and repo disk use are the operator's concern. Document it.

## Left to the builder

Schedule next-run computation and DST handling, concurrency level, staging layout and cleanup, XML/checksum parsing libraries, `createrepo_c --update` tuning, exact JSON field names, UI layout, and the wording of the variable prompts.
