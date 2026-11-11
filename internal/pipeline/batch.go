package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"dnfrepo/internal/rpmmeta"
	"dnfrepo/internal/store"
)

// StagedFile is a verified file outside the repo dir, waiting to be ingested.
type StagedFile struct {
	Path, Filename, SHA256 string
	Size                   int64
}

// IngestBatch places staged rpms into a first-stage repo as one unit (mirror sync path):
// all files are validated first, linked in without overwriting, rows are inserted in one
// transaction, then old versions are purged and metadata rebuilt once. Before the DB commit any
// failure removes every file it placed. Row name/version/release/arch come from the rpm header.
func (s *Service) IngestBatch(ctx context.Context, slug string, batchID int64, files []StagedFile, actor string) ([]store.Package, error) {
	repo, err := s.Store.RepoBySlug(ctx, slug)
	if err != nil {
		return nil, err
	}
	if repo.PreviousID != nil {
		return nil, ErrNotFirstStage
	}
	defer s.lock(slug)()
	dir := s.Publisher.RepoDir(slug)

	pkgs := make([]store.Package, 0, len(files))
	for _, f := range files {
		if !filenameRe.MatchString(f.Filename) {
			return nil, ErrBadFilename
		}
		sum, n, err := hashFile(f.Path)
		if err != nil {
			return nil, err
		}
		if sum != f.SHA256 || n != f.Size {
			return nil, fmt.Errorf("%w: %s", ErrBadChecksum, f.Filename)
		}
		meta, err := rpmmeta.Parse(f.Path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.Filename, err)
		}
		pkgs = append(pkgs, store.Package{
			RepoID: repo.ID, Filename: f.Filename, Name: meta.Name, Version: meta.Version,
			Release: meta.Release, Arch: meta.Arch, SHA256: sum, Size: n,
		})
	}

	var placed []string
	cleanup := func() {
		for _, p := range placed {
			os.Remove(p)
		}
	}
	for _, f := range files {
		final := filepath.Join(dir, f.Filename)
		if err := os.Chmod(f.Path, 0o644); err != nil {
			cleanup()
			return nil, err
		}
		if !s.linkDup(ctx, f.SHA256, f.Size, final) {
			if err := linkOrCopy(f.Path, final); err != nil { // never overwrites: Link and O_EXCL fail on an existing name
				cleanup()
				if os.IsExist(err) {
					return nil, fmt.Errorf("%w: %s", ErrExists, f.Filename)
				}
				return nil, err
			}
		}
		placed = append(placed, final)
	}
	if err := s.Store.AddMirrorPackages(ctx, repo.ID, batchID, pkgs, actor); err != nil {
		cleanup()
		return nil, err
	}
	if _, err := s.purgeLocked(ctx, repo, actor); err != nil {
		return pkgs, fmt.Errorf("batch stored but purging old versions failed: %w", err)
	}
	if err := s.rebuild(ctx, slug); err != nil {
		return pkgs, fmt.Errorf("batch stored but metadata rebuild failed: %w", err)
	}
	return pkgs, nil
}

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n, err
}
