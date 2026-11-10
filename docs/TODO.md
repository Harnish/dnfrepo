# Future work

## Point-in-time repo snapshots (frozen copies)

Keep a frozen, separately served copy of a repo as of a date, for example `/r/<slug>/snapshots/2026-11-05/`, so a client can stay pinned to a past state while newer syncs land.

Heavier than the batch-promotion idea in the mirror design: it needs retained metadata per snapshot (rpms can be shared via hardlinks or content addressing), the same access mode and GPG signing as the parent repo, and a retention policy so snapshots do not grow without bound.

Requested 2026-11-10 while designing mirror repos (sync from an upstream URL or `.repo` file). The first version of mirror repos records each sync as a batch that can be promoted as a unit; frozen snapshot URLs are deliberately out of scope there.

## Mirror: dry-run / preview of a sync

Show what a sync would add (and its size) before downloading or publishing anything.

## Mirror: failover across metalink URLs

A metalink or mirrorlist is resolved once to its first URL at save time. Try the other URLs when that one fails.
