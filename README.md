# dnfrepo

A web service that manages signed dnf/yum repositories: upload rpms or pull them from GitHub releases and Plex, promote them through dev → test → prod, control who can pull from each repo, and hand out ready-made `.repo` files.

## Features

- Many repos, each served at `/r/<slug>/` with `createrepo_c` metadata signed by an instance GPG key (`repo_gpgcheck=1`).
- **Pipelines:** link repos in a chain. Only the first repo takes uploads and sources; later repos get packages only by promoting from the previous one (one or many rpms per promotion, all-or-nothing).
- **Access, one mode per repo:** `public`, `ips` (IP/CIDR allowlist), or `users` (HTTP Basic auth with per-user tokens).
- **Sources** (first stage only): GitHub latest-release rpm (`owner/name`) and Plex Media Server, checked daily or on demand.
- **Retention:** per repo, keep only the newest N versions of each package (default limit plus per-package overrides; 0 = unlimited). Packages are grouped by name and arch and ordered with rpm's version comparison. Enforced after every upload or promotion into the repo and when the limits are saved; each stage has its own limits.
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
| `OIDC_ISSUER` | required | Issuer URL, e.g. `https://auth.example.org/application/o/<slug>/` |
| `OIDC_CLIENT_ID` `OIDC_CLIENT_SECRET` | required | From the Authentik provider |
| `OIDC_ADMIN_GROUP` | `dnfrepo-admins` | Group whose members are admins |
| `GITHUB_TOKEN` | empty | Avoids GitHub API rate limits for sources |
| `MAX_UPLOAD_BYTES` | 1 GiB | Per-rpm upload limit |

TLS is not handled by the app: put it behind a reverse proxy (and set `TRUSTED_PROXIES`). Use HTTPS for `users`-mode repos, since Basic auth sends the token on every request.

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

All `/api` routes except `GET /api/auth/oidc/login` and `/callback` need the session cookie, and state-changing requests need the header `X-Requested-With: dnfrepo`.

| | |
|---|---|
| `GET /api/auth/oidc/login` `callback`; `GET /api/auth/me`; `POST /api/auth/logout` | admin sign-in (OIDC) |
| `GET/POST /api/repos`, `GET/PATCH/DELETE /api/repos/{slug}`, `PUT .../ips`, `PUT .../previous`, `GET/PUT .../users` | repos, pipeline links, access |
| `GET/POST /api/repos/{slug}/packages` (multipart `file`), `DELETE .../packages/{filename}` | packages |
| `POST /api/repos/{slug}/promote` `{"filenames":["foo-1.rpm","bar-2.rpm"]}` | promote to the next stage |
| `GET/POST /api/repos/{slug}/sources`, `DELETE /api/sources/{id}`, `POST /api/sources/{id}/run` | sources |
| `GET/POST /api/repo-users`, `DELETE /api/repo-users/{id}` | repo users and tokens |
| `PUT /api/repos/{slug}/keep` `{"keep":3,"overrides":{"plexmediaserver":10}}` | set retention limits and purge now (returns the purged filenames) |
| `GET /api/repos/{slug}/audit` | activity log |
