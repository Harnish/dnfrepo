package mirror

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"dnfrepo/internal/debmeta"
	"dnfrepo/internal/pipeline"
	"dnfrepo/internal/store"
)

// refresh installs upstream's current index in a cache repo without downloading any package:
// rpm copies the verified repodata, deb regenerates the archive from the verified indexes.
// Filters do not apply: clients see everything upstream lists.
func (s *Syncer) refresh(ctx context.Context, repo store.Repo, up packageSource, actor string) (Result, error) {
	var (
		all []UpstreamPkg
		dir string
		err error
	)
	if rpm, ok := up.(*Upstream); ok {
		if s.StagingDir == "" {
			return Result{}, errors.New("mirror staging dir is not configured")
		}
		parent := filepath.Join(s.StagingDir, strconv.FormatInt(repo.ID, 10))
		if err := os.MkdirAll(parent, 0o700); err != nil {
			return Result{}, fmt.Errorf("staging dir: %w", err)
		}
		dir = filepath.Join(parent, "repodata-"+strconv.FormatInt(time.Now().UnixNano(), 10))
		if err := os.Mkdir(dir, 0o700); err != nil {
			return Result{}, fmt.Errorf("staging dir: %w", err)
		}
		defer os.RemoveAll(dir) // no-op once the swap consumed it
		// nil when nothing is published yet (or unreadable): then always refresh
		current, _ := os.ReadFile(filepath.Join(s.Pipeline.Publisher.RepoDir(repo.Slug), "repodata", "repomd.xml"))
		var unchanged bool
		if all, unchanged, err = rpm.FetchRepodata(ctx, dir, current); err == nil && unchanged {
			return Result{Status: "up to date"}, nil
		}
	} else {
		all, err = up.Packages(ctx)
	}
	if err != nil {
		return Result{}, err
	}
	entries, err := cacheEntries(repo.Kind, all)
	if err != nil {
		return Result{}, err
	}
	if _, err := s.Pipeline.RefreshCache(ctx, repo.Slug, entries, dir, actor); err != nil {
		return Result{}, err
	}
	return Result{Status: "refreshed"}, nil
}

// cacheEntries maps an upstream index to cache entries. A client requests an rpm by its
// upstream location (served verbatim in repodata) and a deb by pool/<component>/<file>
// (the regenerated Packages files use the flat pool layout).
func cacheEntries(kind string, pkgs []UpstreamPkg) ([]store.CacheEntry, error) {
	pkgs, err := dedupe(pkgs)
	if err != nil {
		return nil, err
	}
	out := make([]store.CacheEntry, 0, len(pkgs))
	for _, p := range pkgs {
		e := store.CacheEntry{Path: p.Href, Href: p.Href, Filename: p.Filename, Name: p.Name, Arch: p.Arch,
			Version: p.Version, Release: p.Release, SHA256: p.SHA256, Size: p.Size}
		if kind == store.KindDeb {
			e.Path, e.Component, e.Control = "pool/"+p.Component+"/"+p.Filename, p.Component, p.Control
			st, err := debmeta.ParseStanzas(bytes.NewReader([]byte(p.Control)))
			if err != nil || len(st) != 1 {
				return nil, fmt.Errorf("%s: bad control stanza", p.Filename)
			}
			e.MD5, e.SHA1 = st[0].Fields["MD5sum"], st[0].Fields["SHA1"]
		}
		out = append(out, e)
	}
	return out, nil
}

// Fetch serves a cache miss: it streams e from upstream into w and, for the first request of
// that file, stores it through pipeline.IngestBatch once its size, sha256 and header match the
// index. The fill runs detached from ctx (bounded by Timeout), so a client that disconnects
// still leaves the package cached; w is never written after Fetch returns. A request for a
// file that is already being filled is proxied straight from upstream. The returned error is
// for the client only (scrubbed of the upstream credential); ingest failures are logged.
func (s *Syncer) Fetch(ctx context.Context, repo store.Repo, e store.CacheEntry, w io.Writer) error {
	m, err := s.Store.MirrorByRepo(ctx, repo.ID)
	if err != nil {
		return err
	}
	token, err := s.openToken(m)
	if err != nil {
		return err
	}
	up, _, err := s.source(ctx, m, repo, token)
	if err != nil {
		return err
	}
	p := UpstreamPkg{Name: e.Name, Arch: e.Arch, Version: e.Version, Release: e.Release, Filename: e.Filename,
		Href: e.Href, SHA256: e.SHA256, Size: e.Size, Component: e.Component, Control: e.Control}
	scrubbed := func(err error) error {
		if err == nil {
			return nil
		}
		return &scrubErr{msg: scrub(err.Error(), token), err: err}
	}

	key := strconv.FormatInt(repo.ID, 10) + "/" + e.Filename
	s.fillMu.Lock()
	if s.filling[key] {
		s.fillMu.Unlock()
		return scrubbed(up.Download(ctx, p, w))
	}
	if s.filling == nil {
		s.filling = map[string]bool{}
	}
	s.filling[key] = true
	s.fillMu.Unlock()

	cw := &clientWriter{w: w}
	sent := make(chan error, 1) // the download's outcome, before ingest
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			s.fillMu.Lock()
			delete(s.filling, key)
			s.fillMu.Unlock()
		}()
		d := s.Timeout
		if d <= 0 {
			d = defaultTimeout
		}
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), d)
		defer cancel()
		if err := s.fill(fctx, repo, up, p, cw, sent); err != nil {
			log.Printf("mirror: cache %s: %s", repo.Slug, scrubbed(err))
		}
	}()
	select {
	case err := <-sent:
		cw.detach()
		return scrubbed(err)
	case <-ctx.Done():
		cw.detach()
		return ctx.Err()
	}
}

// fill downloads p into a staging file and w together, reports the download result on sent,
// then ingests the verified file.
func (s *Syncer) fill(ctx context.Context, repo store.Repo, up packageSource, p UpstreamPkg, w io.Writer, sent chan<- error) error {
	if s.StagingDir == "" {
		err := errors.New("mirror staging dir is not configured")
		sent <- err
		return err
	}
	dir := filepath.Join(s.StagingDir, strconv.FormatInt(repo.ID, 10))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		sent <- err
		return err
	}
	path := filepath.Join(dir, p.Filename)
	part := path + ".part"
	f, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		sent <- err
		return err
	}
	err = up.Download(ctx, p, io.MultiWriter(f, w))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(part, path)
	}
	sent <- err
	if err != nil {
		os.Remove(part)
		return err
	}
	defer os.Remove(path)
	files := []pipeline.StagedFile{{Path: path, Filename: p.Filename, SHA256: p.SHA256, Size: p.Size, Component: p.Component, Control: p.Control}}
	if err := checkHeaders(files, []UpstreamPkg{p}); err != nil {
		return err
	}
	_, err = s.Pipeline.IngestBatch(ctx, repo.Slug, 0, files, "cache")
	return err
}

// clientWriter forwards to w until w fails or detach is called; it never fails itself, so a
// gone client cannot abort the fill.
type clientWriter struct {
	mu   sync.Mutex
	w    io.Writer
	done bool
}

func (c *clientWriter) Write(b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.done {
		if _, err := c.w.Write(b); err != nil {
			c.done = true
		}
	}
	return len(b), nil
}

func (c *clientWriter) detach() {
	c.mu.Lock()
	c.done = true
	c.mu.Unlock()
}
