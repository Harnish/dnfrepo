// dnfrepo: web service managing signed dnf/yum repositories.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"dnfrepo/internal/adminauth"
	"dnfrepo/internal/api"
	"dnfrepo/internal/config"
	"dnfrepo/internal/pipeline"
	"dnfrepo/internal/publish"
	"dnfrepo/internal/serve"
	"dnfrepo/internal/signer"
	"dnfrepo/internal/sources"
	"dnfrepo/internal/store"
	"dnfrepo/web"
)

func main() {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()

	sg, err := signer.LoadOrCreate(filepath.Join(cfg.DataDir, "gpg", "signing-key.asc"))
	if err != nil {
		log.Fatal(err)
	}
	pub := publish.New(cfg.DataDir, sg)
	rebuild := func(slug string) {
		if err := pub.Rebuild(context.Background(), slug); err != nil {
			log.Printf("rebuild %s: %v", slug, err)
		}
	}
	// repos without published metadata yet (new, or restored data dir)
	if repos, err := st.ListRepos(ctx); err == nil {
		for _, r := range repos {
			os.MkdirAll(pub.RepoDir(r.Slug), 0o755)
			if _, err := os.Lstat(filepath.Join(pub.RepoDir(r.Slug), "repodata")); err != nil {
				rebuild(r.Slug)
			}
		}
	}

	pl := &pipeline.Service{Store: st, Publisher: pub, MaxBytes: cfg.MaxUploadBytes}
	if repos, err := st.ListRepos(ctx); err == nil {
		for _, r := range repos {
			if moved, err := pl.QuarantineOrphans(ctx, r.Slug); err != nil {
				log.Printf("orphan sweep %s: %v", r.Slug, err)
			} else if len(moved) > 0 {
				log.Printf("orphan sweep %s: moved %v to %s/orphans/%s", r.Slug, moved, cfg.DataDir, r.Slug)
			}
		}
	}

	admin := adminauth.New(st, cfg, adminauth.LazyIDP(cfg)) // provider contacted on first login, not at boot
	sch := &sources.Scheduler{Store: st, Pipeline: pl, GitHubToken: cfg.GitHubToken}
	sch.Start(ctx)

	actor := func(r *http.Request) string { e, _ := admin.Authenticate(r); return e }
	handler := api.Router(api.Deps{
		Admin:     admin,
		Repos:     &api.Repos{Store: st, DataDir: cfg.DataDir, OnChange: rebuild},
		RepoUsers: &api.RepoUsers{Store: st},
		Packages:  &api.Packages{Pipeline: pl, Actor: actor},
		Sources:   &api.Sources{Store: st, Scheduler: sch},
		Serve: serve.Handler(serve.Deps{Store: st, RepoDir: pub.RepoDir, PublicKey: sg.PublicKeyArmored(),
			BaseURL: cfg.BaseURL, Trusted: cfg.TrustedProxies}),
		Static: web.Dist(),
	})

	srv := &http.Server{Addr: cfg.ListenAddr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		srv.Shutdown(sctx)
	}()
	log.Printf("dnfrepo listening on %s", cfg.ListenAddr)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
