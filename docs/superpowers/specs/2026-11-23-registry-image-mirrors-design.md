# Registry image mirrors — design

## Intent

Let the embedded container registry keep a local copy of an upstream image tag, e.g.
`docker.io/plexinc/pms-docker:latest`, and follow it the way a GitHub source follows releases:
check when added, every 24 h, and on demand; copy only when upstream changed. Hosts pull from
dnfrepo instead of Docker Hub (rate limits, outages, bandwidth), and the last few versions stay
available for rollback.

## Goals

- Admin adds an **image mirror**: upstream reference (`[registry/]repo:tag`), local name
  (default: the upstream path, `plexinc/pms-docker`), CPU types to copy, versions to keep
  (default **3**), optional upstream username + token.
- A run checks the upstream tag's digest. Unchanged and the local copy still present →
  status `up to date`, nothing downloaded. Changed (or local copy missing) → copy the selected
  platforms into the local registry, status `downloaded`.
- Each copy is tagged twice: the moving local tag (same name as upstream's, e.g. `latest`) and a
  dated tag `<tag>-YYYYMMDD` (`-2`, `-3`… for a second copy the same UTC day).
- Retention: keep the newest N dated tags this mirror created; delete older ones. The moving tag
  always points at the newest copy. Freed blobs go at the next garbage collection.
- CPU types: the add/edit form lists the platforms the upstream tag publishes (fetched live) as
  checkboxes. Selected subset → the local tag is an index of only those platforms. Nothing
  selected = all. A single-platform upstream image is copied as is.
- Runs: on add (immediately, like sources), with the sources schedule (a minute after start, then
  every 24 h), and **Check now**. Status, last run, last error and current digest show per mirror.

## Non-goals

- Mirroring several tags or all tags of a repo in one entry (add one entry per tag).
- Digest-pinned upstream refs (`@sha256:…`): nothing to follow; rejected.
- Docker schema1 manifests; cosign signature tags (`sha256-….sig`) and the referrers API are not
  copied. (Attestation entries *inside* an index are handled in step 3.)
- Automatic garbage collection after a prune (GC stays the admin's button).
- Per-mirror schedules (rpm mirror style); the 24 h source cadence is enough for now.

## Anti-goals

- Storing anything whose bytes don't match the digest the upstream manifest names.
- Upstream credentials reaching logs, API responses or clients.
- Deleting a tag the mirror did not create (pushed tags with a look-alike name are safe).
- A mirror run blocking or corrupting a garbage collection.

## Approaches

- **A. Own small fetcher over `net/http`, no new dependency (chosen).** Registry v2 pull
  protocol: Bearer-token challenge (anonymous or Basic creds to the token realm) or plain Basic;
  `HEAD`/`GET` manifests, `GET` blobs. Manifest parsing uses Distribution's own `manifest/*`
  packages; writes go straight into the embedded registry's storage (`Namespace`), whose blob
  writer verifies digests. Cost: auth quirks of unusual registries are ours.
- B. go-containerregistry: complete auth and platform handling, but a large new dependency that
  duplicates the Distribution types already embedded. Rejected.
- C. Shell out to `skopeo`: extra binary in the image and an HTTP push back into ourselves with a
  push token. Rejected.

Distribution's pull client is `internal/` (not importable) and its proxy mode mirrors one
upstream for the whole registry, so neither fits.

## Design

### Data (migration `012_registry_mirrors.sql`)

- `registry_mirrors`: `id`, `upstream` (normalized ref, e.g. `docker.io/plexinc/pms-docker`),
  `tag`, `local_name`, `platforms text[]` (`os/arch[/variant]`, empty = all), `keep int`
  (default 3, ≥ 1), `username`, `secret` (sealed with `SECRET_KEY`), `upstream_digest`,
  `last_run`, `last_status`, `last_error`, `created_at`. Unique `(local_name, tag)`.
- `registry_mirror_versions`: `mirror_id` (cascade), `tag` (the dated tag), `digest` (local),
  `upstream_digest`, `created_at`. This is the retention ledger: only these tags are pruned.

### Reference handling

`distribution/reference.ParseNormalizedNamed`: `pms-docker` → `docker.io/library/pms-docker`,
missing tag → `latest`. Docker Hub's API host is `registry-1.docker.io`; other hosts are used as
given over HTTPS. Local name validated like any registry repo name.

### A run (per mirror)

1. Resolve upstream: `HEAD` the tag with Accept for OCI index/manifest and Docker manifest
   list/schema2 → upstream digest. Up to date only if it equals `upstream_digest` **and** the local
   tag still points at the digest this mirror last stored. Otherwise copy.
   *(My call: the mirror owns `local_name:tag`. A tag already there when the mirror is added, or
   pushed over it later, is overwritten by the next run. Pushes are not blocked.)*
2. Steps 3's download happens first (see below); then `Gate.Begin()` for the import. Refused (GC
   running) → status `error`, "registry busy (garbage collection)", retried next run.
3. `GET` the manifest. Index/list with no platform filter (or every platform selected): copy
   **every** entry, attestation manifests (`unknown/unknown`) included, and store the index bytes
   unchanged, so the local digest equals upstream's and `@sha256:` pins keep working. With a
   subset: every selected platform must exist upstream; if any is missing the run fails
   with an error naming it and the previous copy stays (*decided by the user*). Keep the
   matching platform entries, drop attestation entries, and write a new index
   (local digest differs). For each kept child: `GET` its manifest, copy config + layers (skip
   blobs already present), `Put` the child by digest. Single manifest: copy blobs, `Put` it.
   *(Builder check, my call: copy attestations when nothing is filtered, so the digest matches.)*
   **Download outside the gate:** blobs are fetched into `<DATA_DIR>/image-staging/<run>/`
   (removed after the run, also at startup). Only the import into registry storage, a local copy,
   runs under `Gate.Begin()`, so a garbage collection waits seconds, not for a download.
   *(My call: replaces "hold the gate for the whole copy".)*
4. Tag the result `<tag>` and the next free `<tag>-YYYYMMDD[-n]`; insert the version row; set
   `upstream_digest`.
5. Prune: dated tags of this mirror beyond `keep`, oldest first: untag and delete the row.
6. Ensure a `registry_repos` row exists for `local_name`, so it shows in the Registry page. An
   existing row keeps its settings. New row: public + unlisted, **except** for a mirror with
   upstream credentials: `users` mode with no grants, so a private upstream image is not served to
   anyone until the admin opens it. *(Decided by the user.)*

Any error leaves the previous local tag untouched (tags move only after every blob and manifest
is stored). Network timeout as other sources (15 min client); no per-blob size cap beyond that.

### Scheduling

`sources.Scheduler` gains an image-mirror runner; `RunAll` runs sources then image mirrors, each
logged and carried on. Add → `go RunOne` like new sources. Runs for the same mirror don't
overlap (per-mirror in-flight guard; a second request returns "already running").

### API (admin session or API token)

- `GET/POST /api/registry/mirrors`, `PUT/DELETE /api/registry/mirrors/{id}`,
  `POST /api/registry/mirrors/{id}/run`.
- `POST /api/registry/mirrors/inspect` `{upstream, username?, token?}` → `{digest, platforms:[…]}`
  for the form's checkboxes (`[]` for a single-platform image).
- Editable with `PUT`: platforms, keep, credentials. Upstream, tag and local name are fixed
  (delete and re-add to change them), so the ledger's dated tags always belong to one image.
  Changing platforms clears `upstream_digest` and starts a run. *(My call.)*
- `inspect` also accepts `{id}` and then uses that mirror's sealed credentials server-side, so
  *Load platforms* works on the edit form without retyping the token. *(My call.)*
- Credentials write-only: responses say `has_credentials`; omitted on `PUT` keeps them;
  `SECRET_KEY` unset + credentials → 400 (as rpm mirrors).
- Deleting a mirror keeps the local image and tags (the admin deletes those on the image page).

### UI

Registry page: new **Mirrors** table (upstream:tag → local name, platforms, keep, last run,
status/error, current digest short, *Check now*, *Edit*, *Remove*) and an **Add mirror** form:
upstream ref, local name (prefilled from the ref), *Load platforms* → checkboxes, keep (3),
optional username/token.

### Docs

README "Container registry" section: image mirrors; CLAUDE.md invariant line; `docs/api.md`.

## Left to the builder

File layout inside `internal/registry` / `internal/sources`, exact status strings, how the token
cache is kept within a run, UI layout details, test fake for the upstream registry (an
`httptest` server speaking the v2 pull protocol plus token realm).
