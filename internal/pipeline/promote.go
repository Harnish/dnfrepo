package pipeline

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"dnfrepo/internal/store"
)

var (
	ErrNoNextStage    = errors.New("repo has no next stage to promote into")
	ErrConflict       = errors.New("target stage already has a different file with that name")
	ErrEmptyPromotion = errors.New("no filenames given")
)

type PromoteResult struct {
	To       string
	Promoted []string
	Skipped  []string // already present with identical content
}

// Promote copies the named rpms from fromSlug into the next stage as one unit:
// everything is validated first, files are staged and renamed in, rows are
// inserted in one transaction, and the target is rebuilt once. On failure the
// target's files and rows are left as they were.
func (s *Service) Promote(ctx context.Context, fromSlug string, filenames []string, actor string) (PromoteResult, error) {
	var res PromoteResult
	if len(filenames) == 0 {
		return res, ErrEmptyPromotion
	}
	seen := map[string]bool{}
	var names []string
	for _, f := range filenames {
		if !validPackageFilename(f) {
			return res, ErrBadFilename
		}
		if !seen[f] {
			seen[f] = true
			names = append(names, f)
		}
	}
	from, err := s.Store.RepoBySlug(ctx, fromSlug)
	if err != nil {
		return res, err
	}
	to, err := s.Store.NextRepo(ctx, from.ID)
	if err != nil {
		return res, err
	}
	if to == nil {
		return res, ErrNoNextStage
	}
	res.To = to.Slug
	if from.Kind != to.Kind {
		return res, ErrWrongKind
	}
	defer s.lock(to.Slug)()

	var missing []string
	var todo []store.Package
	for _, n := range names {
		src, err := s.Store.PackageByFilename(ctx, from.ID, n)
		if errors.Is(err, store.ErrNotFound) {
			missing = append(missing, n)
			continue
		}
		if err != nil {
			return res, err
		}
		have, err := s.Store.PackageByFilename(ctx, to.ID, n)
		switch {
		case err == nil && have.SHA256 == src.SHA256:
			res.Skipped = append(res.Skipped, n)
		case err == nil:
			return res, fmt.Errorf("%w: %s", ErrConflict, n)
		case errors.Is(err, store.ErrNotFound):
			todo = append(todo, src)
		default:
			return res, err
		}
	}
	if len(missing) > 0 {
		return res, fmt.Errorf("%w: %s", store.ErrNotFound, strings.Join(missing, ", "))
	}
	if len(todo) == 0 {
		return res, nil
	}

	if to.Kind == store.KindDeb {
		d, err := s.Store.DebRepoByID(ctx, to.ID)
		if err != nil {
			return res, err
		}
		for _, p := range todo {
			if !debAccepts(d, p.Component, p.Arch) {
				return res, fmt.Errorf("%w: %s (%s, %s)", ErrDebIncompatible, p.Filename, p.Component, p.Arch)
			}
		}
	}

	srcDir, dstDir := s.Publisher.RepoDir(from.Slug), s.Publisher.RepoDir(to.Slug)
	type staged struct{ tmp, final string }
	var st []staged
	var placed []string
	cleanup := func() {
		for _, x := range st {
			os.Remove(x.tmp)
		}
		for _, p := range placed {
			os.Remove(p)
		}
	}
	for _, p := range todo {
		b := make([]byte, 6)
		rand.Read(b)
		tmp := filepath.Join(dstDir, ".promote-"+hex.EncodeToString(b))
		if err := linkOrCopy(filepath.Join(srcDir, p.Filename), tmp); err != nil {
			cleanup()
			return res, err
		}
		st = append(st, staged{tmp, filepath.Join(dstDir, p.Filename)})
	}
	for _, x := range st {
		if err := os.Link(x.tmp, x.final); err != nil { // never overwrite an existing file
			cleanup()
			return res, err
		}
		placed = append(placed, x.final)
		os.Remove(x.tmp)
	}
	st = nil
	detail := make([]string, len(todo))
	for i, p := range todo {
		detail[i] = p.Filename + " from " + from.Slug
		res.Promoted = append(res.Promoted, p.Filename)
	}
	if err := s.Store.AddPackagesAudited(ctx, to.ID, todo, actor, "promoted-in", detail); err != nil {
		cleanup()
		res.Promoted = nil
		return res, err
	}
	if _, err := s.purgeLocked(ctx, *to, actor); err != nil {
		return res, fmt.Errorf("promoted but purging old versions failed: %w", err)
	}
	if err := s.rebuild(ctx, to.Slug); err != nil {
		return res, fmt.Errorf("promoted but metadata rebuild failed: %w", err)
	}
	return res, nil
}

// PromoteBatch promotes every package in fromSlug that originated from the sync batch.
func (s *Service) PromoteBatch(ctx context.Context, fromSlug string, batchID int64, actor string) (PromoteResult, error) {
	from, err := s.Store.RepoBySlug(ctx, fromSlug)
	if err != nil {
		return PromoteResult{}, err
	}
	names, err := s.Store.BatchFilenames(ctx, from.ID, batchID)
	if err != nil {
		return PromoteResult{}, err
	}
	return s.Promote(ctx, fromSlug, names, actor) // empty -> ErrEmptyPromotion
}

// linkOrCopy hardlinks src to dst, falling back to a copy across filesystems.
func linkOrCopy(src, dst string) error {
	if err := os.Link(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	return out.Close()
}
