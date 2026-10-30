package sources

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"dnfrepo/internal/pipeline"
	"dnfrepo/internal/store"
)

// Scheduler checks every source and lands new releases in the source's (first-stage) repo.
type Scheduler struct {
	Store       *store.Store
	Pipeline    *pipeline.Service
	Client      *http.Client
	Interval    time.Duration // default 24h
	GitHubToken string
	GitHubAPI   string // default https://api.github.com
	PlexURL     string // default PlexURL
}

func (s *Scheduler) client() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return &http.Client{Timeout: 15 * time.Minute}
}

func (s *Scheduler) latest(ctx context.Context, src store.Source) (Release, error) {
	switch src.Kind {
	case "github":
		api := s.GitHubAPI
		if api == "" {
			api = "https://api.github.com"
		}
		return LatestGitHub(ctx, s.client(), api, s.GitHubToken, src.Spec)
	case "plex":
		u := s.PlexURL
		if u == "" {
			u = PlexURL
		}
		return LatestPlex(ctx, s.client(), u)
	}
	return Release{}, fmt.Errorf("unknown source kind %q", src.Kind)
}

// RunOne checks one source and records the outcome on it.
func (s *Scheduler) RunOne(ctx context.Context, id int64) error {
	src, err := s.Store.SourceByID(ctx, id)
	if err != nil {
		return err
	}
	status, err := s.fetch(ctx, src)
	msg := ""
	if err != nil {
		status, msg = "error", err.Error()
	}
	s.Store.RecordSourceRun(ctx, id, status, msg)
	return err
}

func (s *Scheduler) fetch(ctx context.Context, src store.Source) (string, error) {
	repo, err := s.Store.RepoByID(ctx, src.RepoID)
	if err != nil {
		return "", err
	}
	rel, err := s.latest(ctx, src)
	if err != nil {
		return "", err
	}
	if _, err := s.Store.PackageByFilename(ctx, repo.ID, rel.Filename); err == nil {
		return "up to date", nil
	}
	req, err := http.NewRequestWithContext(ctx, "GET", rel.URL, nil)
	if err != nil {
		return "", err
	}
	resp, err := s.client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("download %s: %s", rel.URL, resp.Status)
	}
	tmp, err := os.CreateTemp("", "dnfrepo-src-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		return "", err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	actor := "source:" + src.Kind
	if src.Spec != "" {
		actor += ":" + src.Spec
	}
	if _, err := s.Pipeline.Upload(ctx, repo.Slug, rel.Filename, tmp, actor); err != nil {
		return "", err
	}
	return "downloaded", nil
}

// RunAll runs every source, logging failures and carrying on.
func (s *Scheduler) RunAll(ctx context.Context) {
	srcs, err := s.Store.AllSources(ctx)
	if err != nil {
		log.Printf("sources: list: %v", err)
		return
	}
	for _, src := range srcs {
		if err := s.RunOne(ctx, src.ID); err != nil {
			log.Printf("sources: %s %s: %v", src.Kind, src.Spec, err)
		}
	}
}

// Start runs all sources a minute after boot and then every Interval, until ctx ends.
func (s *Scheduler) Start(ctx context.Context) {
	iv := s.Interval
	if iv <= 0 {
		iv = 24 * time.Hour
	}
	go func() {
		t := time.NewTimer(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.RunAll(ctx)
				t.Reset(iv)
			}
		}
	}()
}
