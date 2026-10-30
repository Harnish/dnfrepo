# dnfrepo: dnf/yum repository management web service

## Intent

Today the author runs `rpmdl` (../github_rpm_downloader): a CLI that pulls the latest x86_64 rpm from a list of GitHub releases (plus Plex) into one directory, runs `createrepo`, and lets nginx serve it. It works, but it is one flat repo, has no access control, no promotion, no UI.

`dnfrepo` replaces that with a web service that manages many dnf/yum repositories: it stores rpms, builds repo metadata with `createrepo_c`, signs it, serves it to dnf clients, and gives admins a UI and API to manage repos, sources, access, and promotion between stages.

Three audiences pull from it: the author's home network, a friend group, and outside users. That is why per-repo access control matters.

## Goals

- Create and manage multiple repos. Each repo is served over HTTP(S) as a standard dnf/yum repo (`repodata/` built by `createrepo_c`, rpms alongside).
- **Pipelines:** an ordered chain of repos (for example dev, test, prod). Only the first repo accepts new packages. Later repos receive packages only by promotion from the previous one.
- **Promotion:** the admin selects one or more specific rpm files in a repo and promotes them to the next repo in one API call or UI action. All-or-nothing.
- **Access control**, one mode per repo, admin-chosen:
  - `public`: anyone.
  - `ips`: allowlist of IPs and CIDR ranges.
  - `users`: HTTP Basic auth with per-user tokens.
  The mode cannot be mixed.
- **Sources** (first-stage repos only): GitHub release sources (same behavior as `rpmdl`) and a built-in Plex Media Server source. A background job checks them on a schedule; new releases land in the first stage.
- **Upload** of rpms by an admin (first-stage repos only), via UI and API.
- **GPG-signed metadata:** `repomd.xml.asc` is generated for every repo; generated `.repo` files set `repo_gpgcheck=1` and point at the public key.
- **Generated `.repo` files** for every repo, downloadable, with credentials embedded for `users`-mode repos.
- **Browsing:** every repo has a browse page listing its packages.
- **RSS feed** per repo.
- Admin web UI (React) and a JSON API that the UI uses and scripts can use too.

## Non-goals

- Signing the rpm packages themselves (no per-package GPG re-signing). Generated `.repo` files use `gpgcheck=0` unless noted; only metadata is signed.
- Non-rpm formats (deb, container images), modular/AppStream repos, delta rpms.
- Multi-tenancy, HA, clustering, mirroring/syncing from other repos.
- Automatic retention/pruning of old versions (manual delete only).
- Importing an existing rpm directory (possible later; not v1).

## Anti-goals (a failure even if it technically works)

- Any path by which a package enters a non-first stage without being promoted from the previous stage (uploads, sources, direct file writes, API tricks, skipping a stage).
- A private repo's rpms, metadata, browse page, feed, or `.repo` file reachable without satisfying that repo's access mode.
- Clients ever seeing half-written metadata, or a promotion that is only partly applied.
- Tokens or the GPG private key appearing in logs, API responses (after creation), or the database in readable form.

## Constraints and stack

Decided by the author (global defaults, plus this conversation):

- Backend in Go. PostgreSQL for state.
- Admin sign-in: email as user id, verified by a verification email sent with `go-mail/mail` (not `net/smtp`).
- Frontend: a JS front end (React) talking to the Go JSON API.
- `createrepo_c` generates repository metadata.
- Metadata is GPG signed.
- Project standards: multi-stage Dockerfile with pinned base images, non-root user, HEALTHCHECK; `.dockerignore`; `.github/workflows/docker-build.yml` (build on push to main, tag with commit SHA and `latest`, push to ghcr.io); `.gitlab-ci.yml` with Docker-in-Docker build and push. Conventional commits; never commit without confirmation.

## Design (the author's decisions)

### Repos, pipelines, stages

- A repo has a slug (used in URLs), display name, access mode, and an optional `previous` repo.
- Chains are linear: a repo has at most one next and one previous; no cycles; a repo outside any chain is its own first stage and accepts uploads and sources.
- "First stage" = a repo with no `previous`. Everything else is a later stage.
- Server-side enforcement: upload and source attachment are rejected for later stages; promotion only goes from a repo to its direct next repo.

### Access modes

- One mode per repo: `public`, `ips`, or `users`.
- The mode applies to everything under the repo's URL: rpms, `repodata/`, the browse page, the RSS feed, and the `.repo` file.
- `users` mode: Basic auth, username plus a token. Admin manages the list of users allowed per repo and can revoke individually.

### Promotion

- Input: a set of specific rpm files in repo A. Output: those same files copied into the next repo B. They stay in A.
- All or nothing: B's metadata is rebuilt and re-signed once after the whole set is in place; clients see either the old state or the new state.
- Every promotion is recorded (who, when, which files, from/to) and appears in the repo's RSS feed.

### Sources

- A source belongs to a first-stage repo.
- GitHub source: owner/name; downloads the x86_64/amd64 `.rpm` from the latest release; if the asset filename has no version-like part (`N.N`), the release tag (without leading `v`) is appended before `.rpm`. Skips files already present. Uses an optional GitHub token from config to avoid rate limits.
- Plex source: built in; downloads the latest Plex Media Server redhat `linux-x86_64` rpm from `https://plex.tv/api/downloads/1.json`.
- Scheduled background checks (default daily) plus a "check now" action; each source records last run, status, and error.

### Serving, URLs

- Repos are served from the Go service itself (range requests required for dnf).
- Layout (builder may adjust names, but keep one URL prefix per repo): `/r/<slug>/` browse page, `/r/<slug>/repodata/...`, `/r/<slug>/<file>.rpm`, `/r/<slug>/<slug>.repo`, `/r/<slug>/feed.xml`, `/gpg.key` public key.
- Admin UI and `/api/...` are separate from repo URLs and are not governed by repo access modes.

## Calls the author did not make (my choices; marked for review)

1. **Repo users are not email accounts.** Admin creates a named repo user; the service generates a random token shown once, stored hashed; used as the Basic-auth password. Tokens are scoped to the repos the user is granted.
2. **One instance-wide GPG key** signs all repos, generated on first start (or imported via config), private key stored on the data volume; signing done in Go (no `gpg` binary). Public key at `/gpg.key`.
3. **Multiple admin accounts** are supported; the first is bootstrapped from an environment variable email, further admins invited by an existing admin.
4. **Client IP for `ips` mode** is the TCP peer address by default; `X-Forwarded-For` is honored only for peers in a configured trusted-proxy list (empty by default).
5. **TLS** is terminated by a reverse proxy; the app speaks HTTP and has a configured `BASE_URL` used in generated `.repo` files. The UI warns if a `users`-mode repo's base URL is plain HTTP.
6. **Promotion copies** files (hardlink when on the same filesystem), never moves them.
7. **Manual delete** of a package is allowed in any stage (admin only), triggering a metadata rebuild; deleting from an earlier stage never removes it from later stages.
8. **Filename collisions:** uploading or promoting a file that already exists is skipped if the checksum matches and is an error if it differs.
9. **Browse page** is server-rendered at `/r/<slug>/` (so a browser or curl works under the same access rules), and the React admin UI links to it.
10. **Failed-auth throttling:** repeated bad Basic-auth attempts from one IP are rate limited.
11. **Rpm validation on upload:** the file must parse as an rpm; name/version/arch come from its header; configurable max upload size.
12. **Storage:** files on disk under a data directory, `<data>/repos/<slug>/`; Postgres holds repos, users, tokens (hashed), sources, packages, and the audit log.

## Left to the builder

- Package layout, routing library, migration tool, React tooling, rpm header parsing library, GPG/openpgp library.
- Exact API shapes and UI design.
- Scheduling implementation and per-repo locking for `createrepo_c` runs.
- Test strategy.
