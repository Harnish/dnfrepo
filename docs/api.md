# dnfrepo API

JSON API used by the admin UI and available to scripts. Examples assume `BASE=https://dnfrepo.example.org` and `TOKEN=dnfr_...`.

## Authentication

Create an **API token** in the UI (API tokens page, shown once) and send it on every request:

```
Authorization: Bearer dnfr_...
```

- A token has full admin API access. Give each script its own and revoke it from the UI when done.
- Bearer requests do not need the `X-Requested-With` header. Browser sessions (cookie) still do on state-changing requests.
- An invalid token is rejected with `401`; it never falls back to a session cookie.
- Tokens are stored only as a SHA-256 hash. The audit log records the actor as `token:<name>`.
- Token management itself (`/api/auth/tokens`) needs a signed-in browser session; a token cannot create or revoke tokens.

Errors are JSON: `{"error":"message"}`.

## Repo kinds

`POST /api/repos` takes `kind` (`rpm` default, or `deb`). For `deb` it also takes `suite` (default slug; `^[a-z0-9][a-z0-9.-]*$`), `components` (default `["main"]`) and `arches` (default `["amd64","arm64"]`, at least one besides `all`); these fields on an rpm repo are `400`. Repo objects return `kind`, and for deb `suite`, `components`, `arches`. `PATCH /api/repos/{slug}` accepts `arches` for deb repos (`409` for rpm); it only affects future uploads, promotions and indexing. A repo can only be linked behind a repo of the same kind (`409`).

## Packages

A package file is an rpm, or for a deb repo a `.deb` named `<Package>_<version without epoch>_<Arch>.deb` (`400` if the name does not match the control data). Deb package objects add `component`, and `version` holds the full Debian version including any epoch, with `release` empty. Promoting into a deb repo that does not accept the package's component or architecture is `409`. A Plex source on a deb repo is `409`.

Rpm filenames must match `^[A-Za-z0-9][A-Za-z0-9._+~-]*\.rpm$`; URL-encode them in paths.

### Upload: `POST /api/repos/{slug}/packages`

`multipart/form-data` with the rpm in the `file` part. The repo must be the **first stage** of its pipeline. Maximum size is `MAX_UPLOAD_BYTES` (default 1 GiB).

```
curl -fsS -H "Authorization: Bearer $TOKEN" -F file=@foo-1.0-1.x86_64.rpm "$BASE/api/repos/dev/packages"
```

`201`:
```json
{"filename":"foo-1.0-1.x86_64.rpm","name":"foo","version":"1.0","release":"1","arch":"x86_64","sha256":"…","size":12345,"added_at":"2026-11-10T20:00:00Z","origin_batch_id":null}
```

| Status | Meaning |
|---|---|
| 400 | no multipart body or no `file` part, not a valid rpm, or bad filename |
| 404 | repo not found |
| 409 | repo is not a first stage, or a file with that name already exists |
| 413 | file larger than `MAX_UPLOAD_BYTES` |

Metadata is rebuilt and signed before the response returns. Retention is applied afterwards.

### List: `GET /api/repos/{slug}/packages`

`200`: array of the objects above.

### Delete: `DELETE /api/repos/{slug}/packages/{filename}`

```
curl -fsS -X DELETE -H "Authorization: Bearer $TOKEN" "$BASE/api/repos/dev/packages/foo-1.0-1.x86_64.rpm"
```

`200`: `{"status":"deleted"}`. `400` bad filename, `404` repo or package not found. Works on any stage. Metadata is rebuilt.

### Promote: `POST /api/repos/{slug}/promote`

Copy files from `{slug}` into its next stage. All-or-nothing.

```
curl -fsS -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"filenames":["foo-1.0-1.x86_64.rpm","bar-2.3-1.noarch.rpm"]}' "$BASE/api/repos/dev/promote"
```

`200`:
```json
{"to":"test","promoted":["foo-1.0-1.x86_64.rpm"],"skipped":["bar-2.3-1.noarch.rpm"]}
```
`skipped` lists files already present in the next stage with identical content.

| Status | Meaning |
|---|---|
| 400 | `filenames` empty, or a bad filename |
| 404 | repo or a named file not found |
| 409 | repo has no next stage, or the next stage already has a *different* file with that name (nothing is promoted) |

**By batch**: send `{"batch":12}` instead of `filenames` to promote every package in `{slug}` that came from sync batch 12 (promoted packages keep their batch, so the same id works in later stages). Give exactly one of the two (`400` otherwise); `404` if the batch has no packages in this repo.

A mirror repo refuses uploads and sources (`409`), and cannot be linked behind another repo (`409`).

## Mirrors

A mirror is an empty first-stage repo that syncs from an upstream dnf repo, or for a deb repo from an apt archive. Credentials are write-only: the token is stored encrypted (needs `SECRET_KEY`) and never returned. Keep tokens out of shell history, e.g. build the body with `jq` from an env var.

### Parse a .repo file: `POST /api/mirror/parse-repo`

```
jq -n --rawfile f fedora.repo '{repo_file:$f}' |
  curl -fsS -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d @- "$BASE/api/mirror/parse-repo"
```

`200 {"sections":[{"id":"fedora","name":"Fedora $releasever","url_kind":"metalink","variables":["basearch","releasever"],"has_credentials":false}]}`. `400` if no section has a baseurl/metalink/mirrorlist.

### Configure: `PUT /api/repos/{slug}/mirror`

Body: `upstream_url` **or** `repo_file` (+ `section` when it has several, `vars` for its `$variables`), and optionally `username`, `token`, `clear_credentials`, `gpg_key` (armored), `require_signature`, `arches`, `include`, `exclude` (globs on package name), `keep_latest` (0 = all), `schedule` `{frequency: manual|daily|weekly|monthly, time_of_day:"HH:MM", timezone, weekdays:[0-6, 0=Sunday], day_of_month, paused}`.

```
jq -n --arg tok "$UPSTREAM_TOKEN" '{upstream_url:"https://pkgs.example.org/el9/x86_64/", username:"ci", token:$tok,
  arches:["x86_64","noarch"], schedule:{frequency:"daily", time_of_day:"03:00", timezone:"UTC"}}' |
  curl -fsS -X PUT -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d @- "$BASE/api/repos/el9/mirror"
```

- Variables are substituted and a metalink/mirrorlist is resolved to its first URL now; the resolved URL is stored. From a `.repo` file an http(s) `gpgkey` is fetched and `repo_gpgcheck=1` sets `require_signature`; `username`/`password` become the credentials.
- PUT replaces every field except the credentials: an omitted `gpg_key`, filter or `schedule` is cleared. Omitting `token` keeps the stored one (this needs `SECRET_KEY`); `token:""` clears it; `clear_credentials:true` removes username and token. Only the first http(s) `gpgkey` URL of a `.repo` section is used.
- `200`: the config (as GET). `400`: bad URL or schedule, `{"error","missing_vars":[...]}`, `{"error","sections":[ids]}` when a section must be chosen, credentials without `SECRET_KEY`, `require_signature` without a key. `409`: repo has packages or sources, or is a later stage.

**Deb mirrors:** `PUT` on a deb repo takes `upstream_url` (the archive base, e.g. `https://deb.debian.org/debian/`), `suite` (required), `components` and `arches` (required, at least one real arch; they are also stored on the repo), plus the credential, `gpg_key`, `require_signature`, `include`, `exclude`, `keep_latest` and `schedule` fields above. `repo_file` is `400` for deb repos, and `suite`/`components` on an rpm repo are `400`. `GET` additionally returns `suite` and `components`. `parse-repo` stays rpm-only.

### Read / remove: `GET`, `DELETE /api/repos/{slug}/mirror`

`GET` returns the config plus `state` (`idle`/`running`), `last_run`, `last_status`, `last_error`, `next_run`, `has_credentials`; `username` is shown, the token never. `DELETE` turns the repo back into a normal first stage, keeping its packages (`409` while a sync runs; deleting the repo itself is also `409` while a sync runs). Both `404` if the repo is not a mirror.

### Sync now: `POST /api/repos/{slug}/mirror/sync`

```
curl -fsS -X POST -H "Authorization: Bearer $TOKEN" "$BASE/api/repos/el9/mirror/sync"
```

`202 {"status":"started"}`; poll `GET .../mirror` for the result. `409` if a sync is running, `404` if not a mirror. Sync writes are audited as `mirror:<slug>`; the request itself as `sync-requested` by the caller.

### Batches: `GET /api/repos/{slug}/batches`

`200 [{"id":12,"started_at":"…","finished_at":"…","status":"synced","added":40,"error":""}]`, newest first (at most 100). Lists the repo's own sync batches and, in later stages, the batches its packages were promoted from; for those, `added` is how many of the batch's packages are in this repo. `origin_batch_id` on a package names its batch.

## Dedup

### Sweep: `POST /api/dedup`

Replaces identical rpm files (same sha256 and size) across all repos with hardlinks to one copy. Both files are re-hashed from disk first; the swap is an atomic rename, so readers never see a missing file. `200 {"linked":3,"bytes_saved":18219,"skipped":0}`. `skipped` counts duplicates left alone (file missing, content mismatch, or on another filesystem). No metadata rebuild is needed. Each affected repo gets a `deduped` audit event. Works whether or not `DEDUP` is set.

## Tokens (browser session only)

| | |
|---|---|
| `GET /api/auth/tokens` | list `[{id,name,created_by,created_at}]` (never the secret) |
| `POST /api/auth/tokens` `{"name":"ci"}` | `201 {"id":1,"name":"ci","token":"dnfr_..."}`; the token is shown only here |
| `DELETE /api/auth/tokens/{id}` | revoke |

## Other endpoints

See the table in the README. All need the same authentication, except `GET /api/auth/methods`, `GET /api/auth/oidc/*` and `POST /api/auth/local`.

## CI example

```
set -euo pipefail
curl -fsS -H "Authorization: Bearer $DNFREPO_TOKEN" -F file=@"$RPM" "$BASE/api/repos/dev/packages"
# after tests pass:
curl -fsS -H "Authorization: Bearer $DNFREPO_TOKEN" -H 'Content-Type: application/json' \
  -d "{\"filenames\":[\"$(basename "$RPM")\"]}" "$BASE/api/repos/dev/promote"
```
