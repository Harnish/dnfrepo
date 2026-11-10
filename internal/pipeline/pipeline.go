// Package pipeline is the only writer of package files: upload, delete, promote.
package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"dnfrepo/internal/rpmmeta"
	"dnfrepo/internal/store"
)

var (
	ErrNotFirstStage = errors.New("repo is not the first stage of its pipeline; packages arrive only by promotion")
	ErrExists        = errors.New("package file already exists")
	ErrBadFilename   = errors.New("invalid rpm filename")
	ErrTooLarge      = errors.New("upload too large")
	ErrBadChecksum   = errors.New("staged file does not match its recorded checksum/size")
)

var filenameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+~-]*\.rpm$`)

// ValidFilename reports whether name is an acceptable rpm file name (no path).
func ValidFilename(name string) bool { return filenameRe.MatchString(name) }

// Publisher is satisfied by *publish.Publisher.
type Publisher interface {
	RepoDir(slug string) string
	Rebuild(ctx context.Context, slug string) error
}

type Service struct {
	Store     *store.Store
	Publisher Publisher
	MaxBytes  int64
	// RebuildTimeout bounds one metadata rebuild; values <= 0 mean 10 minutes.
	RebuildTimeout time.Duration

	locks sync.Map // slug -> *sync.Mutex
}

// lock serializes file placement, DB writes, and rebuild for one repo, so a rebuild
// never publishes files whose operation has not committed (or has been rolled back).
func (s *Service) lock(slug string) (unlock func()) {
	m, _ := s.locks.LoadOrStore(slug, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// rebuild runs detached from the caller's cancellation: once an operation has committed,
// a client disconnect must not leave the published metadata stale.
func (s *Service) rebuild(ctx context.Context, slug string) error {
	d := s.RebuildTimeout
	if d <= 0 {
		d = 10 * time.Minute
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), d)
	defer cancel()
	return s.Publisher.Rebuild(rctx, slug)
}

// Rebuild regenerates slug's metadata under the repo lock; used to retry a failed post-commit rebuild.
func (s *Service) Rebuild(ctx context.Context, slug string) error {
	defer s.lock(slug)()
	return s.rebuild(ctx, slug)
}

func (s *Service) Upload(ctx context.Context, slug, filename string, r io.Reader, actor string) (store.Package, error) {
	if !filenameRe.MatchString(filename) {
		return store.Package{}, ErrBadFilename
	}
	repo, err := s.Store.RepoBySlug(ctx, slug)
	if err != nil {
		return store.Package{}, err
	}
	if repo.PreviousID != nil {
		return store.Package{}, ErrNotFirstStage
	}
	// Fail fast; the real guard is the transactional check in AddPackageFirstStage.
	if mirror, err := s.Store.IsMirror(ctx, repo.ID); err != nil {
		return store.Package{}, err
	} else if mirror {
		return store.Package{}, store.ErrMirrorRepo
	}
	defer s.lock(slug)()
	dir := s.Publisher.RepoDir(slug)
	tmp, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		return store.Package{}, err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(r, s.MaxBytes+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return store.Package{}, err
	}
	if n > s.MaxBytes {
		return store.Package{}, ErrTooLarge
	}
	meta, err := rpmmeta.Parse(tmp.Name())
	if err != nil {
		return store.Package{}, err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return store.Package{}, err
	}
	final := filepath.Join(dir, filename)
	if err := os.Link(tmp.Name(), final); err != nil { // fails if the name is taken
		if os.IsExist(err) {
			return store.Package{}, ErrExists
		}
		return store.Package{}, err
	}
	pkg, err := s.Store.AddPackageFirstStage(ctx, store.Package{
		RepoID: repo.ID, Filename: filename, Name: meta.Name, Version: meta.Version,
		Release: meta.Release, Arch: meta.Arch, SHA256: hex.EncodeToString(h.Sum(nil)), Size: n,
	})
	if err != nil {
		os.Remove(final)
		if errors.Is(err, store.ErrLaterStage) {
			err = ErrNotFirstStage
		}
		return store.Package{}, err
	}
	s.Store.AddAudit(ctx, repo.ID, actor, "uploaded", filename)
	if _, err := s.purgeLocked(ctx, repo, actor); err != nil {
		return pkg, fmt.Errorf("package stored but purging old versions failed: %w", err)
	}
	if err := s.rebuild(ctx, slug); err != nil {
		return pkg, fmt.Errorf("package stored but metadata rebuild failed: %w", err)
	}
	return pkg, nil
}

func (s *Service) Delete(ctx context.Context, slug, filename, actor string) error {
	if !filenameRe.MatchString(filename) {
		return ErrBadFilename
	}
	defer s.lock(slug)()
	repo, err := s.Store.RepoBySlug(ctx, slug)
	if err != nil {
		return err
	}
	if err := s.Store.DeletePackage(ctx, repo.ID, filename); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(s.Publisher.RepoDir(slug), filename)); err != nil && !os.IsNotExist(err) {
		return err
	}
	s.Store.AddAudit(ctx, repo.ID, actor, "deleted", filename)
	return s.rebuild(ctx, slug)
}

// QuarantineOrphans moves rpm files that have no package row (left by a crash between file
// placement and DB commit) to <data>/orphans/<slug>/, removes leftover upload/promote temp
// files, and rebuilds if anything was moved. It returns the moved filenames.
func (s *Service) QuarantineOrphans(ctx context.Context, slug string) ([]string, error) {
	defer s.lock(slug)()
	repo, err := s.Store.RepoBySlug(ctx, slug)
	if err != nil {
		return nil, err
	}
	dir := s.Publisher.RepoDir(slug)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var moved []string
	for _, e := range ents {
		name := e.Name()
		if !e.Type().IsRegular() {
			continue
		}
		if strings.HasPrefix(name, ".upload-") || strings.HasPrefix(name, ".promote-") {
			os.Remove(filepath.Join(dir, name))
			continue
		}
		if !filenameRe.MatchString(name) {
			continue
		}
		if _, err := s.Store.PackageByFilename(ctx, repo.ID, name); err == nil {
			continue
		} else if !errors.Is(err, store.ErrNotFound) {
			return moved, err
		}
		qdir := filepath.Join(filepath.Dir(filepath.Dir(dir)), "orphans", slug)
		if err := os.MkdirAll(qdir, 0o755); err != nil {
			return moved, err
		}
		dst := filepath.Join(qdir, fmt.Sprintf("%s.%d", name, time.Now().UnixNano()))
		if _, err := os.Stat(filepath.Join(qdir, name)); os.IsNotExist(err) {
			dst = filepath.Join(qdir, name)
		}
		if err := os.Rename(filepath.Join(dir, name), dst); err != nil {
			return moved, err
		}
		moved = append(moved, name)
	}
	if len(moved) > 0 {
		return moved, s.rebuild(ctx, slug)
	}
	return moved, nil
}
