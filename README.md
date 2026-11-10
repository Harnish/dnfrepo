# dnfrepo

A web service that manages signed dnf/yum repositories: upload rpms or pull them from GitHub releases and Plex, promote them through dev → test → prod, control who can pull from each repo, and hand out ready-made `.repo` files.

## Features

- Many repos, each served at `/r/<slug>/` with `createrepo_c` metadata signed by an instance GPG key (`repo_gpgcheck=1`).
- **Pipelines:** link repos in a chain. Only the first repo takes uploads and sources; later repos get packages only by promoting from the previous one (one or many rpms per promotion, all-or-nothing).
- **Access, one mode per repo:** `public`, `ips` (IP/CIDR allowlist), or `users` (HTTP Basic auth with per-user tokens).
- **Sources** (first stage only): GitHub latest-release rpm (`owner/name`) and Plex Media Server, checked daily or on demand.
- **Retention:** per repo, keep only the newest N versions of each package (default limit plus per-package overrides; 0 = unlimited). Packages are grouped by name and arch and ordered with rpm's version comparison. Enforced after every upload or promotion into the repo and when the limits are saved; each stage has its own limits.
- **Mirrors:** a repo can mirror an upstream dnf repo (see below).
- Browse page, `<slug>.repo` file and RSS feed per repo, all under that repo's access rule.
- React admin UI and a JSON API under `/api`.

## Run

```
docker build -t dnfrepo .
docker run -d --name dnfrepo -p 8080:8080 -v dnfrepo-data:/data \
  -e DATABASE_URL=postgres://user:pass@db/dnfrepo \
  -e BASE_URL=https://repo.example.org \
  -e OIDC_ISSUER=https://auth.example.org/application/o/dnfrepo/ \
  -e OIDC_CLIENT_ID=... -e OIDC_CLIENT_SECRET=... \
  dnfrepo
```

Admins sign in through OpenID Connect (built for Authentik). Only members of the `OIDC_ADMIN_GROUP` group can get in; the group is checked at login, so removing someone takes effect when their session expires (12 hours).

Authentik setup: create an OAuth2/OpenID Provider (confidential client) with redirect URI `<BASE_URL>/api/auth/oidc/callback` and scopes `openid email profile`, bind it to an Application, and create the admin group (default name `dnfrepo-admins`). The ID token must carry a `groups` claim; Authentik's default `profile` scope mapping provides it. The provider is contacted on first login, not at boot, so repo serving keeps working while Authentik is down (admin login returns 503 until it's back).

Admin sign-in limits to know about:

- **Removal is not instant.** Group membership is read only at login. Removing or disabling someone in Authentik does not end a session they already have; it lasts up to 12 hours. To cut access sooner, delete their rows: `DELETE FROM admin_sessions WHERE email = '<their email>';`.
- **Audit attribution uses the `email` claim.** Access is decided only by the `groups` claim, but the audit log records the email. An admin who can edit their own email in Authentik could appear in the log as another admin. Disable self-service email editing in Authentik (user settings stage) if attribution matters to you.

| Variable | Default | Meaning |
|---|---|---|
| `DATABASE_URL` | required | PostgreSQL DSN |
| `BASE_URL` | required | Public URL, used in `.repo` files and the OIDC redirect URI |
| `LISTEN_ADDR` | `:8080` | HTTP listen address |
| `DATA_DIR` | `/data` | rpms, metadata, and the GPG key (`gpg/signing-key.asc`, mode 0600) — back this up |
| `TRUSTED_PROXIES` | empty | Comma-separated IPs/CIDRs of reverse proxies whose `X-Forwarded-For` is trusted (needed for `ips` mode behind a proxy) |
| `ADMIN_EMAIL` `ADMIN_PASSWORD` | empty | Homelab bypass: set both and the login page shows an email/password form instead of Authentik; the `OIDC_*` vars then become optional. One shared admin, same 12 h session and per-IP throttle. Use a long random password and keep it in a Secret |
| `OIDC_ISSUER` | required (unless local admin set) | Issuer URL, e.g. `https://auth.example.org/application/o/<slug>/` |
| `OIDC_CLIENT_ID` `OIDC_CLIENT_SECRET` | required (unless local admin set) | From the Authentik provider |
| `OIDC_ADMIN_GROUP` | `dnfrepo-admins` | Group whose members are admins |
| `GITHUB_TOKEN` | empty | Avoids GitHub API rate limits for sources |
| `MAX_UPLOAD_BYTES` | 1 GiB | Per-rpm upload limit |
| `MIRROR_MAX_RPM_BYTES` | 4 GiB | Per-rpm limit for mirror downloads |
| `SECRET_KEY` | empty | Seals mirror upstream credentials (username/token) in the database. Needed only when a mirror uses credentials. If you remove or change it later, stored credentials can no longer be decrypted: clear them and enter them again |
| `REBUILD_TIMEOUT` | `10m` | Cap for one metadata rebuild (Go duration, e.g. `30m`); raise it for very large mirrors |

TLS is not handled by the app: put it behind a reverse proxy (and set `TRUSTED_PROXIES`). Use HTTPS for `users`-mode repos, since Basic auth sends the token on every request.

## Mirror repos

A mirror is an empty first-stage repo that copies an upstream dnf repo. Give it a base URL, or paste a `.repo` file and pick a section. Variables such as `$releasever` and `$basearch` are resolved once, when you save; a metalink or mirrorlist is resolved to its first URL then. Only the first `gpgkey` URL of a `.repo` section is used. Fedora example: one mirror per release and repo (for example `fedora-42` and `updates-42`), synced on the schedule you test against.

- **Filters:** architectures, include/exclude globs on package name, and keep-latest N versions.
- **Schedule:** manual, daily, weekly or monthly, with a time and time zone. A schedule missed while the service was down runs once at startup. Sync now runs on demand.
- **Signature:** optionally verify upstream metadata with a GPG key. The mirror re-signs its own metadata with the instance key, so clients use this server's `/gpg.key`.
- **Credentials:** optional username and token for the upstream, stored sealed with `SECRET_KEY` and never returned by the API.
- **Batches:** every sync is a numbered batch. A batch can be promoted to the next stage as a unit, and again from each later stage it reaches.
- **First sync:** a sync publishes only when it is fully downloaded and verified, so a big repo shows nothing until the whole first sync finishes. A sync stops after 6 hours, so a very large first sync may take more than one run; files already staged and verified are reused, not downloaded again. If a sync published but its metadata rebuild failed, the next sync retries the rebuild even when nothing new is upstream. Syncs are additive: upstream deletions are not mirrored, and a package you delete by hand comes back at the next sync.
- **Disk:** the mirror's size and the staging directory (`<DATA_DIR>/mirror-staging`) are the operator's concern. Staging is not cleaned up automatically. Internal and private upstream URLs are allowed (admins are trusted).
- **Editing:** saving replaces every field except the credentials. Incremental `createrepo_c --update` exists but is off (no measured speedup). Frozen snapshot URLs are not supported yet (see `docs/TODO.md`).

## Client setup

```
sudo dnf config-manager addrepo --from-repofile=https://repo.example.org/r/<slug>/<slug>.repo
```

`users`-mode `.repo` files contain `username=YOUR_USERNAME` / `password=YOUR_TOKEN` placeholders. The Repo users page can download one with a new user's credentials filled in.

## Development

```
podman run -d --rm -p 5433:5432 -e POSTGRES_PASSWORD=pw postgres:17-alpine
export DNFREPO_TEST_DSN=postgres://postgres:pw@localhost:5433/postgres
go test ./...                       # DB tests skip when the variable is unset
cd web && npm install && npm test && npm run build   # builds web/dist, embedded into the binary
```

Requires `createrepo_c` on `PATH` for the publish tests and for running the service outside the container.

## API

Scripts authenticate with an admin API token (`Authorization: Bearer dnfr_...`, created on the API tokens page). Full reference for upload, delete and promote, with examples and error codes: [docs/api.md](docs/api.md). The admin UI uses the session cookie instead, and its state-changing requests need the header `X-Requested-With: dnfrepo`. Public routes: `GET /api/auth/methods`, `GET /api/auth/oidc/login` and `/callback`, `POST /api/auth/local`.

| | |
|---|---|
| `GET /api/auth/oidc/login` `callback`; `POST /api/auth/local`; `GET /api/auth/me`; `POST /api/auth/logout` | admin sign-in (OIDC, or local when `ADMIN_EMAIL`/`ADMIN_PASSWORD` set) |
| `GET/POST /api/auth/tokens`, `DELETE /api/auth/tokens/{id}` | admin API tokens (browser session only) |
| `GET/POST /api/repos`, `GET/PATCH/DELETE /api/repos/{slug}`, `PUT .../ips`, `PUT .../previous`, `GET/PUT .../users` | repos, pipeline links, access |
| `GET/POST /api/repos/{slug}/packages` (multipart `file`), `DELETE .../packages/{filename}` | packages |
| `POST /api/repos/{slug}/promote` `{"filenames":["foo-1.rpm","bar-2.rpm"]}` or `{"batch":12}` | promote to the next stage |
| `POST /api/mirror/parse-repo`, `GET/PUT/DELETE /api/repos/{slug}/mirror`, `POST .../mirror/sync`, `GET .../batches` | mirror repos (see docs/api.md) |
| `GET/POST /api/repos/{slug}/sources`, `DELETE /api/sources/{id}`, `POST /api/sources/{id}/run` | sources |
| `GET/POST /api/repo-users`, `DELETE /api/repo-users/{id}` | repo users and tokens |
| `PUT /api/repos/{slug}/keep` `{"keep":3,"overrides":{"plexmediaserver":10}}` | set retention limits and purge now (returns the purged filenames) |
| `GET /api/repos/{slug}/audit` | activity log |
