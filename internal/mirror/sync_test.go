package mirror

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"dnfrepo/internal/pipeline"
	"dnfrepo/internal/secretbox"
	"dnfrepo/internal/store"
	"dnfrepo/internal/store/storetest"
)

// syncPub is a pipeline.Publisher that counts rebuilds and can fail them.
type syncPub struct {
	dir      string
	mu       sync.Mutex
	rebuilds int
	err      error
}

func (p *syncPub) RepoDir(slug string) string { return filepath.Join(p.dir, slug) }
func (p *syncPub) Rebuild(ctx context.Context, slug string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rebuilds++
	return p.err
}

type syncEnv struct {
	t    *testing.T
	st   *store.Store
	pub  *syncPub
	up   *fakeUpstream
	s    *Syncer
	repo store.Repo
}

func newSyncEnv(t *testing.T, opts fakeOpts, cfg func(*store.Mirror)) *syncEnv {
	t.Helper()
	ctx := context.Background()
	st := storetest.New(t)
	pub := &syncPub{dir: t.TempDir()}
	repo, err := st.CreateRepo(ctx, "m", "m", store.ModePublic, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(pub.RepoDir("m"), 0o755); err != nil {
		t.Fatal(err)
	}
	up := newFakeUpstream(t, opts)
	m := store.Mirror{RepoID: repo.ID, UpstreamURL: up.URL(), Schedule: store.Schedule{Frequency: "manual", TimeOfDay: "00:00", Timezone: "UTC"}}
	if cfg != nil {
		cfg(&m)
	}
	if err := st.SetMirror(ctx, m); err != nil {
		t.Fatal(err)
	}
	s := &Syncer{Store: st, Pipeline: &pipeline.Service{Store: st, Publisher: pub}, StagingDir: filepath.Join(t.TempDir(), "staging")}
	return &syncEnv{t: t, st: st, pub: pub, up: up, s: s, repo: repo}
}

func (e *syncEnv) run(ctx context.Context) (Result, error) {
	e.t.Helper()
	if err := e.st.ClaimMirror(context.Background(), e.repo.ID); err != nil {
		e.t.Fatal(err)
	}
	return e.s.Run(ctx, e.repo.ID, "mirror:m")
}

func (e *syncEnv) mirror() store.Mirror {
	e.t.Helper()
	m, err := e.st.MirrorByRepo(context.Background(), e.repo.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	return m
}

func (e *syncEnv) rows() []store.Package {
	e.t.Helper()
	p, err := e.st.ListPackages(context.Background(), e.repo.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	return p
}

func (e *syncEnv) batches() []store.Batch {
	e.t.Helper()
	b, err := e.st.ListBatches(context.Background(), e.repo.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	return b
}

func (e *syncEnv) stageDir() string {
	return filepath.Join(e.s.StagingDir, strconv.FormatInt(e.repo.ID, 10))
}

func (e *syncEnv) repoFiles() []string       { return filesIn(e.pub.RepoDir("m")) }
func (e *syncEnv) stagedFiles() []string     { return filesIn(e.stageDir()) }
func (e *syncEnv) add(nevra string) *fakePkg { return addFixture(e.t, e.up, nevra) }

// assertUnchanged checks a failed sync published nothing and finished the mirror as an error.
func (e *syncEnv) assertUnchanged() {
	e.t.Helper()
	if r := e.rows(); len(r) != 0 {
		e.t.Errorf("rows=%v", r)
	}
	if f := e.repoFiles(); len(f) != 0 {
		e.t.Errorf("repo files=%v", f)
	}
	if e.pub.rebuilds != 0 {
		e.t.Errorf("rebuilds=%d", e.pub.rebuilds)
	}
	m := e.mirror()
	if m.State != "idle" || m.LastStatus != "error" || m.LastError == "" {
		e.t.Errorf("mirror state=%q status=%q err=%q", m.State, m.LastStatus, m.LastError)
	}
}

func filesIn(dir string) []string {
	ents, _ := os.ReadDir(dir)
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

func fixture(t *testing.T, nevra string) []byte {
	t.Helper()
	for _, d := range []string{"../rpmmeta/testdata/", "testdata/"} {
		if b, err := os.ReadFile(d + nevra + ".rpm"); err == nil {
			return b
		}
	}
	t.Fatalf("no fixture %s", nevra)
	return nil
}

// addFixture adds the real rpm fixture "name-ver-rel.arch" to the upstream.
func addFixture(t *testing.T, up *fakeUpstream, nevra string) *fakePkg {
	t.Helper()
	d := strings.LastIndex(nevra, ".")
	rest, arch := nevra[:d], nevra[d+1:]
	i := strings.LastIndex(rest, "-")
	j := strings.LastIndex(rest[:i], "-")
	return up.AddPkg(rest[:j], rest[j+1:i], rest[i+1:], arch, fixture(t, nevra))
}

func TestRunSyncsAndCreatesBatch(t *testing.T) {
	e := newSyncEnv(t, fakeOpts{}, nil)
	e.add("hello-1.2.3-4.noarch")
	e.add("other-2.0-1.noarch")
	res, err := e.run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "synced" || res.Added != 2 || res.BatchID == 0 {
		t.Fatalf("res=%+v", res)
	}
	rows := e.rows()
	if len(rows) != 2 {
		t.Fatalf("rows=%v", rows)
	}
	for _, p := range rows {
		if p.OriginBatchID == nil || *p.OriginBatchID != res.BatchID {
			t.Errorf("origin batch %v", p.OriginBatchID)
		}
	}
	if f := e.repoFiles(); len(f) != 2 {
		t.Errorf("repo files=%v", f)
	}
	if e.pub.rebuilds != 1 {
		t.Errorf("rebuilds=%d", e.pub.rebuilds)
	}
	b := e.batches()
	if len(b) != 1 || b[0].Status != "synced" || b[0].AddedCount != 2 || b[0].FinishedAt == nil {
		t.Errorf("batches=%+v", b)
	}
	m := e.mirror()
	if m.State != "idle" || m.LastStatus != "synced" || m.LastError != "" || m.LastRun == nil {
		t.Errorf("mirror=%+v", m)
	}
	if f := e.stagedFiles(); len(f) != 0 {
		t.Errorf("staged left=%v", f)
	}
	if fi, err := os.Stat(e.stageDir()); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("staging dir mode: %v %v", fi.Mode(), err)
	}
}

func TestRunUpToDateCreatesNoBatch(t *testing.T) {
	e := newSyncEnv(t, fakeOpts{}, nil)
	e.add("hello-1.2.3-4.noarch")
	if _, err := e.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	res, err := e.run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "up to date" || res.BatchID != 0 || res.Added != 0 {
		t.Fatalf("res=%+v", res)
	}
	if b := e.batches(); len(b) != 1 {
		t.Errorf("batches=%+v", b)
	}
	if e.pub.rebuilds != 1 {
		t.Errorf("rebuilds=%d", e.pub.rebuilds)
	}
	if m := e.mirror(); m.LastStatus != "up to date" || m.State != "idle" {
		t.Errorf("mirror=%+v", m)
	}
}

func TestRunDownloadFailureLeavesRepoUnchangedAndKeepsStaged(t *testing.T) {
	e := newSyncEnv(t, fakeOpts{}, nil)
	a := e.add("hello-1.2.3-4.noarch")
	b := e.add("other-2.0-1.noarch")
	c := e.add("hello-1.0.0-1.x86_64")
	e.up.Drop(c.Href)
	if _, err := e.run(context.Background()); err == nil {
		t.Fatal("want error")
	}
	e.assertUnchanged()
	if bs := e.batches(); len(bs) != 0 {
		t.Errorf("batches=%+v", bs)
	}
	staged := e.stagedFiles()
	if len(staged) != 2 {
		t.Fatalf("staged=%v", staged)
	}
	for _, f := range staged {
		if strings.HasSuffix(f, ".part") {
			t.Errorf("part left: %s", f)
		}
	}

	delete(e.up.Dropped, c.Href)
	ha, hb := e.up.Hits(a.Href), e.up.Hits(b.Href)
	res, err := e.run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "synced" || res.Added != 3 {
		t.Fatalf("res=%+v", res)
	}
	if e.up.Hits(a.Href) != ha || e.up.Hits(b.Href) != hb {
		t.Errorf("staged files fetched again: a %d->%d b %d->%d", ha, e.up.Hits(a.Href), hb, e.up.Hits(b.Href))
	}
	if f := e.stagedFiles(); len(f) != 0 {
		t.Errorf("staged left=%v", f)
	}
}

func TestRunRehashesStagedFileBeforeTrusting(t *testing.T) {
	e := newSyncEnv(t, fakeOpts{}, nil)
	a := e.add("hello-1.2.3-4.noarch")
	c := e.add("other-2.0-1.noarch")
	e.up.Drop(c.Href)
	if _, err := e.run(context.Background()); err == nil {
		t.Fatal("want error")
	}
	staged := filepath.Join(e.stageDir(), sum(a.Body))
	if err := os.WriteFile(staged, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	delete(e.up.Dropped, c.Href)
	ha := e.up.Hits(a.Href)
	if _, err := e.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.up.Hits(a.Href) != ha+1 {
		t.Errorf("tampered staged file not refetched")
	}
}

func TestRunChecksumMismatchPublishesNothing(t *testing.T) {
	e := newSyncEnv(t, fakeOpts{}, nil)
	e.add("hello-1.2.3-4.noarch")
	bad := e.add("other-2.0-1.noarch")
	e.up.Corrupt(bad)
	_, err := e.run(context.Background())
	if !errors.Is(err, ErrChecksum) {
		t.Fatalf("err=%v", err)
	}
	e.assertUnchanged()
	for _, f := range e.stagedFiles() {
		if f == sum(bad.Body) || strings.HasSuffix(f, ".part") {
			t.Errorf("unverified file staged: %s", f)
		}
	}
}

func TestRunConflictingSameNameFileFails(t *testing.T) {
	e := newSyncEnv(t, fakeOpts{}, nil)
	p := e.add("hello-1.2.3-4.noarch")
	if _, err := e.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	p.Body = fixture(t, "hello-1.9.0-1.noarch") // same filename, different content
	files, hits := e.repoFiles(), e.up.Hits(p.Href)
	_, err := e.run(context.Background())
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("err=%v", err)
	}
	if m := e.mirror(); m.LastStatus != "error" {
		t.Errorf("mirror=%+v", m)
	}
	if len(e.batches()) != 1 || len(e.rows()) != 1 {
		t.Errorf("repo changed")
	}
	if got := e.repoFiles(); strings.Join(got, ",") != strings.Join(files, ",") {
		t.Errorf("repo files %v -> %v", files, got)
	}
	if e.pub.rebuilds != 1 {
		t.Errorf("rebuilds=%d", e.pub.rebuilds)
	}
	if n := e.up.Hits(p.Href); n != hits {
		t.Errorf("downloaded %d rpms", n-hits)
	}
}

func TestStageAllRejectsUnsafeChecksum(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "1")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(root, "x")
	if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Syncer{}
	up := &Upstream{Base: "http://127.0.0.1:1/"}
	for _, sha := range []string{"../x", strings.Repeat("A", 64), ""} {
		_, err := s.stageAll(context.Background(), up, dir, []UpstreamPkg{{Filename: "x.rpm", Href: "x.rpm", SHA256: sha, Size: 4}})
		if err == nil || !strings.Contains(err.Error(), "invalid sha256") {
			t.Errorf("%q: err=%v", sha, err)
		}
	}
	if b, err := os.ReadFile(victim); err != nil || string(b) != "keep" {
		t.Errorf("file outside staging touched: %q %v", b, err)
	}
	if f := filesIn(dir); len(f) != 0 {
		t.Errorf("staged=%v", f)
	}
}

func TestRunRejectsEmptyStagingDir(t *testing.T) {
	e := newSyncEnv(t, fakeOpts{}, nil)
	e.add("hello-1.2.3-4.noarch")
	e.s.StagingDir = ""
	_, err := e.run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "staging dir") {
		t.Fatalf("err=%v", err)
	}
	e.assertUnchanged()
}

func TestRunRespectsKeepNoRedownload(t *testing.T) {
	e := newSyncEnv(t, fakeOpts{}, func(m *store.Mirror) { m.KeepLatest = 3 })
	var pkgs []*fakePkg
	for _, v := range []string{"1.2.3-4", "1.9.0-1", "1.10.0-1", "1.11.0-1", "1.12.0-1"} {
		pkgs = append(pkgs, e.add("hello-"+v+".noarch"))
	}
	res, err := e.run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 3 {
		t.Fatalf("res=%+v", res)
	}
	for _, p := range pkgs[:2] {
		if n := e.up.Hits(p.Href); n != 0 {
			t.Errorf("%s downloaded %d times", p.Filename(), n)
		}
	}
	before := 0
	for _, p := range pkgs {
		before += e.up.Hits(p.Href)
	}
	res, err = e.run(context.Background())
	if err != nil || res.Status != "up to date" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	after := 0
	for _, p := range pkgs {
		after += e.up.Hits(p.Href)
	}
	if after != before {
		t.Errorf("second run downloaded %d", after-before)
	}
	if len(e.rows()) != 3 {
		t.Errorf("rows=%d", len(e.rows()))
	}
}

func TestRunDoesNotDeleteWhenUpstreamDropsPackage(t *testing.T) {
	e := newSyncEnv(t, fakeOpts{}, nil)
	e.add("hello-1.2.3-4.noarch")
	e.add("other-2.0-1.noarch")
	if _, err := e.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.up.Pkgs = e.up.Pkgs[:1]
	res, err := e.run(context.Background())
	if err != nil || res.Status != "up to date" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if len(e.rows()) != 2 || len(e.repoFiles()) != 2 {
		t.Errorf("rows=%d files=%v", len(e.rows()), e.repoFiles())
	}
}

func sealed(t *testing.T, box *secretbox.Box, s string) []byte {
	t.Helper()
	b, err := box.Seal(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRunWithCredentialsDecryptsAndSendsAuth(t *testing.T) {
	box, _ := secretbox.New("k")
	const tok = "tok-abc-123"
	e := newSyncEnv(t, fakeOpts{Username: "u", Password: tok}, func(m *store.Mirror) {
		m.Username = "u"
		m.TokenEnc = sealed(t, box, tok)
	})
	e.s.Box = box
	e.add("hello-1.2.3-4.noarch")
	res, err := e.run(context.Background())
	if err != nil || res.Added != 1 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	for _, r := range e.up.Requests {
		if u, p, ok := r.BasicAuth(); !ok || u != "u" || p != tok {
			t.Errorf("%s: auth %q %q %v", r.URL.Path, u, p, ok)
		}
	}
}

func TestRunCredentialsWithoutSecretKeyFails(t *testing.T) {
	box, _ := secretbox.New("k")
	const tok = "tok-abc-123"
	e := newSyncEnv(t, fakeOpts{}, func(m *store.Mirror) { m.TokenEnc = sealed(t, box, tok) })
	e.add("hello-1.2.3-4.noarch")
	_, err := e.run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "secret key") || strings.Contains(err.Error(), tok) {
		t.Fatalf("err=%v", err)
	}
	if len(e.up.Requests) != 0 {
		t.Errorf("upstream contacted")
	}
	e.assertUnchanged()
}

func TestRunErrorNeverContainsToken(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	box, _ := secretbox.New("k")
	const tok = "SeCrEtToKeN42"
	e := newSyncEnv(t, fakeOpts{Username: "u", Password: tok}, func(m *store.Mirror) {
		m.Username = "u"
		m.TokenEnc = sealed(t, box, tok)
	})
	e.s.Box = box
	// The upstream echoes the token in a package URL, so download and ingest errors would carry it.
	p := e.add("hello-1.2.3-4.noarch")
	p.Href = "Packages/" + tok + "/" + tok + "-hello.rpm"
	e.up.Drop(p.Href)

	check := func(err error, scrubbed bool) {
		t.Helper()
		if err == nil {
			t.Fatal("want error")
		}
		m := e.mirror()
		texts := map[string]string{"returned": err.Error(), "last_error": m.LastError, "log": logs.String()}
		for _, b := range e.batches() {
			texts["batch"] += b.Error
		}
		for k, v := range texts {
			if strings.Contains(v, tok) {
				t.Errorf("%s contains token: %s", k, v)
			}
		}
		if scrubbed && !strings.Contains(m.LastError, "***") {
			t.Errorf("last_error not scrubbed: %q", m.LastError)
		}
	}

	_, err := e.run(context.Background()) // 404 on a URL holding the token
	check(err, true)

	// Ingest fails after the batch exists: a file of that name is already in the repo dir.
	delete(e.up.Dropped, p.Href)
	if err := os.WriteFile(filepath.Join(e.pub.RepoDir("m"), tok+"-hello.rpm"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = e.run(context.Background())
	check(err, true)
	if b := e.batches(); len(b) != 1 || b[0].Status != "error" || !strings.Contains(b[0].Error, "***") {
		t.Errorf("batches=%+v", b)
	}

	// Wrong password at the upstream: still nothing leaks.
	e.up.opts.Password = "other"
	_, err = e.run(context.Background())
	check(err, false)
}

func TestRunSignatureFailureFails(t *testing.T) {
	e := newSyncEnv(t, fakeOpts{Sign: true}, nil)
	if err := e.st.SetMirror(context.Background(), store.Mirror{RepoID: e.repo.ID, UpstreamURL: e.up.URL(), GPGKey: e.up.PubKey, RequireSig: true,
		Schedule: store.Schedule{Frequency: "manual", TimeOfDay: "00:00", Timezone: "UTC"}}); err != nil {
		t.Fatal(err)
	}
	e.add("hello-1.2.3-4.noarch")
	e.up.BadSig = true
	_, err := e.run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("err=%v", err)
	}
	e.assertUnchanged()
}

func TestRunDedupesUpstreamDuplicateFilenames(t *testing.T) {
	t.Run("same checksum", func(t *testing.T) {
		e := newSyncEnv(t, fakeOpts{}, nil)
		p := e.add("hello-1.2.3-4.noarch")
		e.up.Pkgs = append(e.up.Pkgs, &fakePkg{Name: p.Name, Ver: p.Ver, Rel: p.Rel, Arch: p.Arch, Href: "other/" + p.Filename(), Body: p.Body})
		res, err := e.run(context.Background())
		if err != nil || res.Added != 1 {
			t.Fatalf("res=%+v err=%v", res, err)
		}
	})
	t.Run("different checksum", func(t *testing.T) {
		e := newSyncEnv(t, fakeOpts{}, nil)
		p := e.add("hello-1.2.3-4.noarch")
		e.up.Pkgs = append(e.up.Pkgs, &fakePkg{Name: p.Name, Ver: p.Ver, Rel: p.Rel, Arch: p.Arch, Href: "other/" + p.Filename(), Body: fixture(t, "hello-1.9.0-1.noarch")})
		_, err := e.run(context.Background())
		if err == nil || !strings.Contains(err.Error(), p.Filename()) {
			t.Fatalf("err=%v", err)
		}
		e.assertUnchanged()
	})
}

func TestRunRejectsHeaderNEVRAMismatch(t *testing.T) {
	e := newSyncEnv(t, fakeOpts{}, nil)
	e.add("other-2.0-1.noarch")
	e.up.AddPkg("hello", "1.2.3", "4", "noarch", fixture(t, "hello-1.9.0-1.noarch")) // metadata lies about the version
	_, err := e.run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "header") {
		t.Fatalf("err=%v", err)
	}
	e.assertUnchanged()
	if b := e.batches(); len(b) != 0 {
		t.Errorf("batches=%+v", b)
	}
}

func TestRunCommittedButRebuildFailed(t *testing.T) {
	e := newSyncEnv(t, fakeOpts{}, nil)
	e.pub.err = errors.New("createrepo exploded")
	e.add("hello-1.2.3-4.noarch")
	res, err := e.run(context.Background())
	if err == nil {
		t.Fatal("want error")
	}
	if res.Status != "synced" || res.Added != 1 || res.BatchID == 0 {
		t.Fatalf("res=%+v", res)
	}
	if b := e.batches(); len(b) != 1 || b[0].Status != "synced" || b[0].AddedCount != 1 {
		t.Errorf("batches=%+v", b)
	}
	m := e.mirror()
	if m.LastStatus != "error" || !strings.HasPrefix(m.LastError, "published but post-processing failed: ") || m.State != "idle" {
		t.Errorf("mirror status=%q err=%q", m.LastStatus, m.LastError)
	}
	if len(e.rows()) != 1 {
		t.Errorf("rows=%d", len(e.rows()))
	}
	if f := e.stagedFiles(); len(f) != 0 {
		t.Errorf("staged left=%v", f)
	}
}

func TestRunRetriesFailedRebuildWhenNothingNew(t *testing.T) {
	e := newSyncEnv(t, fakeOpts{}, nil)
	e.pub.err = errors.New("createrepo exploded")
	e.add("hello-1.2.3-4.noarch")
	if _, err := e.run(context.Background()); err == nil {
		t.Fatal("want error")
	}
	// Still failing: nothing new, but the retried rebuild fails, so the status stays error.
	if _, err := e.run(context.Background()); err == nil {
		t.Fatal("want error")
	}
	if m := e.mirror(); m.LastStatus != "error" || e.pub.rebuilds != 2 {
		t.Fatalf("status=%q rebuilds=%d", m.LastStatus, e.pub.rebuilds)
	}
	e.pub.err = nil
	res, err := e.run(context.Background())
	if err != nil || res.Status != "up to date" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if e.pub.rebuilds != 3 {
		t.Errorf("rebuilds=%d", e.pub.rebuilds)
	}
	if m := e.mirror(); m.LastStatus != "up to date" {
		t.Errorf("status=%q", m.LastStatus)
	}
	if _, err := e.run(context.Background()); err != nil || e.pub.rebuilds != 3 {
		t.Errorf("healthy up-to-date run rebuilt again: rebuilds=%d err=%v", e.pub.rebuilds, err)
	}
}

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRunCtxCancelStillFinishesMirror(t *testing.T) {
	e := newSyncEnv(t, fakeOpts{}, nil)
	e.add("hello-1.2.3-4.noarch")
	e.add("other-2.0-1.noarch")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.s.Client = &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, ".rpm") {
			cancel()
		}
		return http.DefaultTransport.RoundTrip(r)
	})}
	_, err := e.run(ctx)
	if err == nil {
		t.Fatal("want error")
	}
	e.assertUnchanged()
	for _, f := range e.stagedFiles() {
		if strings.HasSuffix(f, ".part") {
			t.Errorf("part left: %s", f)
		}
	}
}
