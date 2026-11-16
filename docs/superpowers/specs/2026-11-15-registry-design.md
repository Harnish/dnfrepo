# Container registry: design

## Intent

dnfrepo already manages signed rpm and deb repos. The owner also runs a stock `registry:latest` on their k3s (no auth, public at `registry.whiskeyonthe.rocks`, holding personal and third-party images). The registry works, but has no management or user UI and no access control. This feature makes dnfrepo host the registry itself, so one service gives: a manage UI, a read-only public browse page, and enforced per-repo access.

## Goals

- dnfrepo embeds the Distribution registry (`github.com/distribution/distribution/v3`, spiked with v3.1.2: `handlers.NewApp` returns an `http.Handler`, filesystem driver only, ~34 MB binary, Go 1.26 compatible). One process, no sidecar, no reverse proxy hop.
- Registry data stays in the same on-disk layout (filesystem driver), so the existing data directory (`/disks/Data2/podman/registry`) is reused with no migration.
- `/v2/` is served by dnfrepo on the registry hostname; the old standalone registry goes away at cutover.
- Per-image-repository settings (the repo is the full name, e.g. `dnfrepo/dnfrepo`):
  - **Listed**: shows on the public browse page. Default off.
  - **Access**: `public` / `ips` / `users`, same semantics as rpm repos (reuse `internal/access`). Default `public`.
  - For `users` mode the repo has its own user list (pull only), like rpm repo users.
- Enforcement is real, not cosmetic: manifests, blobs, tag lists and the catalog all obey the repo's access mode. Catalog and tag lists are filtered to what the caller may pull.
- **Push is never anonymous.** Admins issue global **push tokens** (create and push to any repo, and pull). A first push to a new name creates its settings row with defaults (public pull, not Listed).
- Admin UI (existing React app, behind OIDC admin sign-in): repo list, per-repo detail (tags, size, created, digest, architectures, layers, copyable pull command), toggle Listed, set Access and users, delete tag, run garbage collection, manage push tokens.
- Public browse page: read-only, lists only Listed repos and their tags with the copyable pull command. Shows nothing for unlisted repos, and never reveals their existence.
- Garbage collection from the admin UI, in process (`storage.MarkAndSweep`). While GC runs the registry is read-only (pushes and deletes get 503, pulls continue), then resumes.
- Optional: registry is enabled by config, so rpm/deb-only deployments are unchanged. Helm chart gets values for it.

## Non-goals

- No namespace or prefix defaults for settings: per repo only.
- No per-repo push permissions or per-user push scopes; push tokens are global.
- No pull-through cache/proxy, no replication, no image signing/scanning, no multi-tenant quotas.
- No scheduled or automatic GC; manual only.
- No non-filesystem storage drivers (S3 etc.).
- Not a replacement for Docker Hub-style user accounts; `users` mode users are per repo, pull only.

## Anti-goals (this fails even if it technically works)

- An unlisted or `users`-mode repo's name, tags or content leaking to someone not allowed (catalog, tag list, error messages that distinguish "exists" from "denied", the public page).
- Any anonymous write.
- Breaking existing rpm/deb behaviour, or existing pulls: cluster nodes pull anonymously today and must keep working for public repos.
- Existing stored images becoming unreadable or lost at cutover.
- Tokens or passwords in logs, responses after creation, or readable in the DB (same invariant as the rest of dnfrepo).
- A registry bug or large upload taking down repo serving or the admin UI (limit body handling, timeouts and keep registry errors isolated).

## Constraints

- Follows project invariants in `CLAUDE.md`: tokens hashed, secrets never logged, access per repo exactly one of public/ips/users, `X-Forwarded-For` only from `TRUSTED_PROXIES`, auth throttle counts the attempt before checking.
- Docker clients authenticate by Basic auth (`docker login`) with a push token; no separate token-service endpoint is required. The registry's auth is a registered `AccessController` that delegates to dnfrepo's access logic.
- New numbered migration (next after `009_deb.sql`), never edit applied ones. TDD per project convention.
- Storage layout and delete behaviour must be compatible with the existing data: delete must be enabled in the registry config for tag deletion and GC.
- Single replica, `Recreate` deployment, hostPath data stays as today.
- Dependency footprint: import only the filesystem driver; keep the module versioned and pinned.

## Decided by the user

Keep the current registry data; front it with dnfrepo so it can filter; hidden-by-default Listed toggle; read-only public browse page; fully enforced access (public/ips/users per repo); push never anonymous; embed the registry as a library; settings per repo; global push tokens with pull-only `users`; first push creates a repo with default public pull; manual GC.

## My calls (flagged for review)

- Defaults: Access `public`, Listed off, for existing and new repos. Rationale: cluster nodes and `build.sh` pull anonymously today.
- Push token model: global, hashed, shown once at creation, revocable, audited (reuse the existing token handling style).
- GC pauses pushes (503) rather than blocking pulls.
- Browse and manage UIs live in the existing React app; the public page is a separate unauthenticated route.
- A repo with zero tags after GC keeps its settings row (so listed/access survive empty states); admins can delete the row.
- **`users`-mode credentials:** reuse the existing `repo_users` (global users, hashed token, revocable, used as the Basic-auth password, `docker login -u <name> -p <token>`). A new join table grants a repo_user pull on a registry repo. Push tokens are a separate table of the same shape.
- **Auth handshake and error policy:** Docker only sends credentials after a 401 with `WWW-Authenticate: Basic`. An anonymous request to any name that is not anonymously readable, or does not exist, gets 401, so existence is not revealed. After authentication, a name the caller may not pull is also 401/denied, never 404.
- **Routing:** `/v2/` is mounted on the existing router and listener (no clash with existing routes), reached on the registry hostname via an extra Ingress host. The public browse page is a path route in the React app, not host-based. Registry data root is a separate mount from `DATA_DIR`.
- **Existing repos at cutover:** no settings rows are created in bulk. The admin repo list is computed live from the storage catalog joined with settings; a repo with no row has the defaults. A row is created on first push or first admin edit. No startup reconciliation.
- **Cutover:** no dual-running. Stop (scale to 0) the old registry, start dnfrepo against the same data, verify; the old Deployment stays on disk for rollback.
- **Delete:** push tokens cannot delete through the HTTP API (the access controller refuses DELETE). Tag delete and GC are admin-UI only and call storage directly.
- **Catalog:** dnfrepo serves `/v2/_catalog` itself: filter first, then paginate (`n`/`last`, `Link`). Anonymous callers see only repos that are anonymously readable AND Listed. Authenticated callers see every repo they may pull, listed or not. Tag lists follow the repo's access mode.
- **GC gate:** on start, new writes and deletes get 503 immediately; wait up to 60 s for in-flight uploads to finish, else abort GC and report it. Pulls continue. The gate lifts when GC ends or aborts.
- **Limits:** the registry route has no body-size cap and no write timeout (the server only sets `ReadHeaderTimeout` today); keep that, and do not add a global write timeout.
- **Throttle:** push-token and `users` auth failures use the existing Basic-auth throttle (attempt counted before checking, per client IP honoring `TRUSTED_PROXIES`).

## Left to the builder

Package layout under `internal/` (e.g. a `registry` package), table/column names, API paths and shapes, how the access controller maps requested repo/action to `internal/access`, how list filtering wraps the handler (catalog pagination `n`/`last`, tag lists), reading image metadata (size, created, architectures, layers) from manifests and configs, React component structure, Helm values names, config env var names, and the cutover runbook (point the Ingress at dnfrepo, retire the old registry deployment, `docker login` for `build.sh`).

## Cutover (for the runbook)

1. Deploy dnfrepo with the registry enabled, using the existing data directory, on a staging hostname; verify pulls of existing images.
2. Move `registry.whiskeyonthe.rocks` to dnfrepo; remove the old registry Deployment/Service/Ingress. The registry UI deployment (`registryui`) can be retired.
3. Create a push token and `docker login` for `~/bin/build.sh` before the next build.
