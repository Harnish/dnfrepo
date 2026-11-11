package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"dnfrepo/internal/store"
)

// linkDup hardlinks final to an existing on-disk copy of the same content when Dedup is on.
// It reports whether it did; any failure (no copy, file missing, other filesystem, name taken)
// returns false so the caller falls back to placing its own copy.
// ponytail: re-hashes the candidate on every ingest; cache by (inode, mtime) if that gets slow.
func (s *Service) linkDup(ctx context.Context, sum string, size int64, final string) bool {
	if !s.Dedup {
		return false
	}
	locs, err := s.Store.PackagesBySHA(ctx, sum, size)
	if err != nil {
		return false
	}
	for _, l := range locs {
		src := filepath.Join(s.Publisher.RepoDir(l.Slug), l.Filename)
		if got, n, err := hashFile(src); err != nil || got != sum || n != size {
			continue
		}
		if os.Link(src, final) == nil {
			return true
		}
	}
	return false
}

// SweepResult summarizes one DedupSweep.
type SweepResult struct {
	Linked     int   // duplicate files replaced by a hardlink
	BytesSaved int64 // disk space reclaimed
	Skipped    int   // duplicates left alone: file missing, content mismatch, or other filesystem
}

// DedupSweep replaces identical package files (same sha256 and size) across all repos with
// hardlinks to one copy. Both files are re-hashed from disk before anything is replaced, the
// replacement is an atomic rename (readers never see a missing file), and it works one repo
// at a time under that repo's lock. Metadata is unchanged, so no rebuild.
// ponytail: serial, re-reads the canonical file per duplicate; fine for a manual sweep.
func (s *Service) DedupSweep(ctx context.Context, actor string) (SweepResult, error) {
	var res SweepResult
	locs, err := s.Store.DuplicatePackages(ctx)
	if err != nil {
		return res, err
	}
	canon := map[string]string{} // "sha:size" -> verified path
	type tally struct {
		n     int
		bytes int64
	}
	per := map[int64]*tally{}
	for _, l := range locs {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		path := filepath.Join(s.Publisher.RepoDir(l.Slug), l.Filename)
		key := fmt.Sprintf("%s:%d", l.SHA256, l.Size)
		c, ok := canon[key]
		if !ok { // first member whose file really has this content becomes the shared copy
			if got, n, err := hashFile(path); err == nil && got == l.SHA256 && n == l.Size {
				canon[key] = path
			} else {
				res.Skipped++
			}
			continue
		}
		linked, skipped := s.linkOver(l, c, path)
		if skipped {
			res.Skipped++
		}
		if linked {
			res.Linked++
			res.BytesSaved += l.Size
			t := per[l.RepoID]
			if t == nil {
				t = &tally{}
				per[l.RepoID] = t
			}
			t.n++
			t.bytes += l.Size
		}
	}
	for id, t := range per {
		s.Store.AddAudit(ctx, id, actor, "deduped", fmt.Sprintf("%d files, %d bytes", t.n, t.bytes))
	}
	return res, nil
}

// linkOver atomically replaces path with a hardlink to canon, if path has the recorded content.
func (s *Service) linkOver(l store.PackageLoc, canon, path string) (linked, skipped bool) {
	defer s.lock(l.Slug)()
	ci, err := os.Stat(canon)
	if err != nil {
		return false, true
	}
	di, err := os.Stat(path)
	if err != nil {
		return false, true
	}
	if os.SameFile(ci, di) {
		return false, false // already shared
	}
	if got, n, err := hashFile(path); err != nil || got != l.SHA256 || n != l.Size {
		return false, true
	}
	tmp := filepath.Join(filepath.Dir(path), ".dedup-"+filepath.Base(path)+".tmp") // not *.rpm: never published
	os.Remove(tmp)
	if os.Link(canon, tmp) != nil { // e.g. different filesystem
		return false, true
	}
	if os.Rename(tmp, path) != nil {
		os.Remove(tmp)
		return false, true
	}
	return true, false
}
