// Package publish builds and signs repo metadata and swaps it in atomically.
package publish

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"dnfrepo/internal/signer"
	"dnfrepo/internal/store"
)

type Publisher struct {
	dataDir string
	signer  *signer.Signer
	// Incremental makes Rebuild reuse the previous repodata (createrepo_c --update).
	// Off by default: measured no faster than a full build (see commit perf(publish)).
	Incremental bool
	// Store lets Rebuild tell deb repos from rpm repos; nil means every repo is an rpm repo.
	Store *store.Store
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func New(dataDir string, s *signer.Signer) *Publisher {
	return &Publisher{dataDir: dataDir, signer: s, locks: map[string]*sync.Mutex{}}
}

func (p *Publisher) RepoDir(slug string) string { return filepath.Join(p.dataDir, "repos", slug) }

func (p *Publisher) lock(slug string) *sync.Mutex {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.locks[slug] == nil {
		p.locks[slug] = &sync.Mutex{}
	}
	return p.locks[slug]
}

// Rebuild regenerates <repo>/repodata via createrepo_c, signs repomd.xml, and
// repoints the repodata symlink at the new generation. Serialized per repo.
// The previous generation is kept so in-flight client downloads don't 404.
func (p *Publisher) Rebuild(ctx context.Context, slug string) error {
	if !store.ValidSlug(slug) {
		return fmt.Errorf("invalid slug %q", slug)
	}
	l := p.lock(slug)
	l.Lock()
	defer l.Unlock()

	if p.Store != nil {
		repo, err := p.Store.RepoBySlug(ctx, slug)
		if err != nil {
			return fmt.Errorf("look up repo %s: %w", slug, err)
		}
		if repo.Kind == store.KindDeb {
			return p.rebuildApt(ctx, repo)
		}
	}
	dir := p.RepoDir(slug)
	gen := fmt.Sprintf("%d", time.Now().UnixNano())
	build := filepath.Join(dir, ".build-"+gen)
	if err := os.MkdirAll(build, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(build)
	args := []string{"-q", "--outputdir", build}
	old := filepath.Join(dir, "repodata")
	if _, err := os.Stat(filepath.Join(old, "repomd.xml")); p.Incremental && err == nil {
		args = append(args, "--update", "--update-md-path", old)
	}
	out, err := exec.CommandContext(ctx, "createrepo_c", append(args, dir)...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("createrepo_c: %v: %s", err, out)
	}
	newDir := filepath.Join(dir, "repodata."+gen)
	if err := os.Rename(filepath.Join(build, "repodata"), newDir); err != nil {
		return err
	}
	repomd, err := os.ReadFile(filepath.Join(newDir, "repomd.xml"))
	if err != nil {
		return err
	}
	sig, err := p.signer.SignDetached(repomd)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(newDir, "repomd.xml.asc"), sig, 0o644); err != nil {
		return err
	}
	tmpLink := filepath.Join(dir, ".link-"+gen)
	if err := os.Symlink("repodata."+gen, tmpLink); err != nil {
		return err
	}
	if err := os.Rename(tmpLink, filepath.Join(dir, "repodata")); err != nil {
		os.Remove(tmpLink)
		return err
	}
	return pruneGenerations(dir, "repodata.")
}

// pruneGenerations keeps the two newest <prefix><n> directories ("repodata." or "dists.").
func pruneGenerations(dir, prefix string) error {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var gens []string
	for _, e := range ents {
		if e.IsDir() && strings.HasPrefix(e.Name(), prefix) {
			gens = append(gens, e.Name())
		}
	}
	sort.Strings(gens) // nanosecond timestamps share a digit count
	for len(gens) > 2 {
		os.RemoveAll(filepath.Join(dir, gens[0]))
		gens = gens[1:]
	}
	return nil
}
