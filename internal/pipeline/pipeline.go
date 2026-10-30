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

	"dnfrepo/internal/rpmmeta"
	"dnfrepo/internal/store"
)

var (
	ErrNotFirstStage = errors.New("repo is not the first stage of its pipeline; packages arrive only by promotion")
	ErrExists        = errors.New("package file already exists")
	ErrBadFilename   = errors.New("invalid rpm filename")
	ErrTooLarge      = errors.New("upload too large")
)

var filenameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+~-]*\.rpm$`)

// Publisher is satisfied by *publish.Publisher.
type Publisher interface {
	RepoDir(slug string) string
	Rebuild(ctx context.Context, slug string) error
}

type Service struct {
	Store     *store.Store
	Publisher Publisher
	MaxBytes  int64
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
	pkg, err := s.Store.AddPackage(ctx, store.Package{
		RepoID: repo.ID, Filename: filename, Name: meta.Name, Version: meta.Version,
		Release: meta.Release, Arch: meta.Arch, SHA256: hex.EncodeToString(h.Sum(nil)), Size: n,
	})
	if err != nil {
		os.Remove(final)
		return store.Package{}, err
	}
	s.Store.AddAudit(ctx, repo.ID, actor, "uploaded", filename)
	if err := s.Publisher.Rebuild(ctx, slug); err != nil {
		return pkg, fmt.Errorf("package stored but metadata rebuild failed: %w", err)
	}
	return pkg, nil
}

func (s *Service) Delete(ctx context.Context, slug, filename, actor string) error {
	if !filenameRe.MatchString(filename) {
		return ErrBadFilename
	}
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
	return s.Publisher.Rebuild(ctx, slug)
}
