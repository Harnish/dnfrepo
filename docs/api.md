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

## Packages

A package file is an rpm. Filenames must match `^[A-Za-z0-9][A-Za-z0-9._+~-]*\.rpm$`; URL-encode them in paths.

### Upload: `POST /api/repos/{slug}/packages`

`multipart/form-data` with the rpm in the `file` part. The repo must be the **first stage** of its pipeline. Maximum size is `MAX_UPLOAD_BYTES` (default 1 GiB).

```
curl -fsS -H "Authorization: Bearer $TOKEN" -F file=@foo-1.0-1.x86_64.rpm "$BASE/api/repos/dev/packages"
```

`201`:
```json
{"filename":"foo-1.0-1.x86_64.rpm","name":"foo","version":"1.0","release":"1","arch":"x86_64","sha256":"…","size":12345,"added_at":"2026-11-10T20:00:00Z"}
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
