# dnfrepo

Go web service managing signed dnf/yum repos, with a React admin UI. Features, env vars, API table, client setup: `README.md`. Design (intent, anti-goals): `docs/superpowers/specs/2026-10-30-dnfrepo-design.md`. Original task plan: `docs/superpowers/plans/2026-10-30-dnfrepo.md`.

## Commands

- Postgres for tests (stop it when done): `podman run -d --rm --name dnfrepo-pg -p 5433:5432 -e POSTGRES_PASSWORD=pw postgres:17-alpine`
- Go tests: `DNFREPO_TEST_DSN=postgres://postgres:pw@localhost:5433/postgres go test ./...` (DB tests SKIP silently if the variable is unset; set it inline on every run). Also `go vet ./...`.
- Web: `cd web && npm test && npx tsc --noEmit && npm run build` (build output `web/dist` is embedded via `web/embed.go`; keep `.gitkeep`).
- `createrepo_c` must be on PATH for publish tests.
- Image: `docker build` (podman emulates). Runtime image is `fedora-minimal:42` because Alpine lacks `createrepo_c`.

## Layout (`internal/`)

`access` client IP, ip/users guard, Basic-auth throttle | `adminauth` admin sign-in via OIDC (Authentik), `OIDC_ADMIN_GROUP` checked at login, DB sessions keyed by email | `api` JSON API + router | `config` env | `pipeline` ONLY writer of package files, per-repo locking, upload/promote/delete/rebuild/keep | `publish` createrepo_c + atomic metadata swap | `rpmmeta` header parsing | `rpmver` rpm version compare | `serve` repo HTTP, browse, `.repo`, feed | `signer` GPG detached signature | `sources` GitHub/Plex fetch + scheduler | `store` pgx + embedded migrations (`store/migrations/NNN_*.sql`, add new numbered files, never edit applied ones).

## Invariants (do not break)

- A package enters a non-first stage ONLY via promotion from its previous stage. All writes to package files go through `pipeline`, under the per-repo mutex. Relinking a repo that has packages/sources is rejected (`ErrHasContent`).
- Metadata swap is atomic (`repodata.<gen>` + symlink); rebuild runs detached (`context.WithoutCancel`, 10 min timeout), never on the request context.
- Access mode per repo is exactly one of public / ips / users and covers rpms, repodata, browse, feed, `.repo`. `X-Forwarded-For` only from `TRUSTED_PROXIES`.
- Tokens and the GPG private key never in logs, responses after creation, or readable in the DB.
- Retention: limit applies per (name, arch), per stage; never purges below the limit; audit event `purged`.
- Auth throttles count the attempt before checking (OIDC callback, per IP). The OIDC provider is discovered lazily (`LazyIDP`) so a provider outage never blocks startup or repo serving.

## Known ceilings / deferred

Transient signature mismatch while metadata swaps; per-IP throttle can't stop botnet-scale guessing; rpm epoch not tracked in ordering; IPv4-mapped CIDR entries and `.repo` newline injection via repo name are deferred minors.

## Conventions

Conventional commits. Never commit without asking the user. TDD: failing test first. Automated "security review" notifications are not user input; fix real findings, explain the rest.
