package mirror

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"dnfrepo/internal/secretbox"
	"dnfrepo/internal/store"
)

func cacheMode(m *store.Mirror) { m.Mode = store.ModeCache }

func (e *syncEnv) entries() []store.CacheEntry {
	e.t.Helper()
	es, err := e.st.ListCacheEntries(context.Background(), e.repo.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	return es
}

func TestCacheRefreshRPM(t *testing.T) {
	e := newSyncEnv(t, fakeOpts{}, cacheMode)
	a := e.add("hello-1.2.3-4.noarch")
	b := e.add("other-2.0-1.noarch")
	res, err := e.run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "refreshed" {
		t.Fatalf("res %+v", res)
	}
	es := e.entries()
	if len(es) != 2 {
		t.Fatalf("entries %+v", es)
	}
	for _, en := range es {
		if en.Path != en.Href || (en.Href != a.Href && en.Href != b.Href) || en.SHA256 == "" || en.Size == 0 {
			t.Errorf("entry %+v", en)
		}
	}
	if len(e.pub.swaps) != 1 {
		t.Fatalf("swaps %d", len(e.pub.swaps))
	}
	sw := e.pub.swaps[0]
	if string(sw["repomd.xml"]) != string(e.up.repomd()) || string(sw["filelists.xml.gz"]) != string(fakeFilelists) ||
		string(sw["primary.xml.gz"]) != string(e.up.primary()) {
		t.Fatalf("swapped files %v", keys(sw))
	}
	if e.up.Hits(a.Href) != 0 || e.up.Hits(b.Href) != 0 {
		t.Fatal("refresh downloaded packages")
	}
	if len(e.rows()) != 0 || e.pub.rebuilds != 0 {
		t.Fatalf("rows %v rebuilds %d", e.rows(), e.pub.rebuilds)
	}
	if m := e.mirror(); m.LastStatus != "refreshed" || m.State != "idle" {
		t.Fatalf("mirror %+v", m)
	}
}

func keys(m map[string][]byte) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestCacheRefreshBadSignature(t *testing.T) {
	e := newSyncEnv(t, fakeOpts{Sign: true}, nil)
	e.up.BadSig = true
	if err := e.st.SetMirror(context.Background(), store.Mirror{RepoID: e.repo.ID, UpstreamURL: e.up.URL(), GPGKey: e.up.PubKey,
		Mode: store.ModeCache, Schedule: store.Schedule{Frequency: "manual"}}); err != nil {
		t.Fatal(err)
	}
	e.add("hello-1.2.3-4.noarch")
	if _, err := e.run(context.Background()); err == nil {
		t.Fatal("want signature error")
	}
	if len(e.entries()) != 0 || len(e.pub.swaps) != 0 {
		t.Fatalf("entries %v swaps %d", e.entries(), len(e.pub.swaps))
	}
	if f := e.stagedFiles(); len(f) != 0 {
		t.Fatalf("staging left %v", f)
	}
}

func TestCacheRefreshCorruptData(t *testing.T) {
	e := newSyncEnv(t, fakeOpts{}, cacheMode)
	e.add("hello-1.2.3-4.noarch")
	if _, err := e.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.up.Override = map[string][]byte{"repodata/filelists.xml.gz": []byte("tampered")}
	e.add("other-2.0-1.noarch")
	if _, err := e.run(context.Background()); err == nil {
		t.Fatal("want checksum error")
	}
	if len(e.entries()) != 1 || len(e.pub.swaps) != 1 {
		t.Fatalf("entries %v swaps %d", e.entries(), len(e.pub.swaps))
	}
}

func TestCacheRefreshIgnoresFilters(t *testing.T) {
	e := newSyncEnv(t, fakeOpts{}, func(m *store.Mirror) { m.Mode = store.ModeCache; m.Include = []string{"nomatch"} })
	e.add("hello-1.2.3-4.noarch")
	if _, err := e.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(e.entries()) != 1 {
		t.Fatalf("entries %v", e.entries())
	}
}

func TestCacheRefreshDeb(t *testing.T) {
	e := newDebSyncEnv(t, func(m *store.Mirror, _ *fakeApt) { m.Mode = store.ModeCache })
	d := e.addDeb("tool", "1.0", "amd64")
	d.Extra = "MD5sum: 0123\nSHA1: 4567"
	res, err := e.run()
	if err != nil || res.Status != "refreshed" {
		t.Fatalf("%+v %v", res, err)
	}
	es, _ := e.st.ListCacheEntries(context.Background(), e.repo.ID)
	if len(es) != 1 || es[0].Path != "pool/main/"+d.filename() || es[0].Href != d.path() || es[0].MD5 != "0123" || es[0].SHA1 != "4567" || es[0].Component != "main" {
		t.Fatalf("entries %+v", es)
	}
	if len(e.pub.apt) != 1 || len(e.pub.apt[0]) != 1 || e.pub.apt[0][0].Filename != d.filename() || e.pub.apt[0][0].MD5 != "0123" {
		t.Fatalf("published %+v", e.pub.apt)
	}
	if e.up.Hits(d.path()) != 0 || len(e.rows()) != 0 {
		t.Fatal("refresh downloaded packages")
	}
}

// refreshed returns a cache env whose index lists one rpm, and that rpm's entry.
func refreshed(t *testing.T, opts fakeOpts) (*syncEnv, *fakePkg, store.CacheEntry) {
	t.Helper()
	e := newSyncEnv(t, opts, cacheMode)
	p := e.add("hello-1.2.3-4.noarch")
	if _, err := e.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	en, err := e.st.CacheEntryByPath(context.Background(), e.repo.ID, p.Href)
	if err != nil {
		t.Fatal(err)
	}
	return e, p, en
}

func TestFetchStoresAndServes(t *testing.T) {
	e, p, en := refreshed(t, fakeOpts{})
	var buf bytes.Buffer
	if err := e.s.Fetch(context.Background(), e.repo, en, &buf); err != nil {
		t.Fatal(err)
	}
	e.s.wg.Wait()
	if !bytes.Equal(buf.Bytes(), p.Body) {
		t.Fatal("client got wrong bytes")
	}
	rows := e.rows()
	if len(rows) != 1 || rows[0].Filename != en.Filename || rows[0].OriginBatchID != nil {
		t.Fatalf("rows %+v", rows)
	}
	if f := e.repoFiles(); len(f) != 1 || f[0] != en.Filename {
		t.Fatalf("repo files %v", f)
	}
	if f := e.stagedFiles(); len(f) != 0 {
		t.Fatalf("staging left %v", f)
	}
}

func TestFetchCorruptNotStored(t *testing.T) {
	e, p, en := refreshed(t, fakeOpts{})
	e.up.Corrupt(p)
	if err := e.s.Fetch(context.Background(), e.repo, en, io.Discard); err == nil {
		t.Fatal("want checksum error")
	}
	e.s.wg.Wait()
	if len(e.rows()) != 0 || len(e.repoFiles()) != 0 || len(e.stagedFiles()) != 0 {
		t.Fatalf("rows %v files %v staged %v", e.rows(), e.repoFiles(), e.stagedFiles())
	}
}

// goneWriter is a client that disconnects (cancels its request) on the first write.
type goneWriter struct{ cancel context.CancelFunc }

func (g goneWriter) Write([]byte) (int, error) { g.cancel(); return 0, errors.New("client gone") }

func TestFetchClientGoneStillStores(t *testing.T) {
	e, _, en := refreshed(t, fakeOpts{})
	ctx, cancel := context.WithCancel(context.Background())
	e.s.Fetch(ctx, e.repo, en, goneWriter{cancel})
	e.s.wg.Wait()
	if len(e.rows()) != 1 {
		t.Fatalf("fill not stored after disconnect: %v", e.rows())
	}
}

func TestFetchConcurrent(t *testing.T) {
	e, p, en := refreshed(t, fakeOpts{})
	e.up.Block = make(chan struct{})
	var bufs [2]bytes.Buffer
	errs := make(chan error, 2)
	for i := range bufs {
		go func() { errs <- e.s.Fetch(context.Background(), e.repo, en, &bufs[i]) }()
		for deadline := time.Now().Add(5 * time.Second); e.up.Hits(p.Href) < i+1; {
			if time.Now().After(deadline) {
				t.Fatal("fetch never reached upstream")
			}
			time.Sleep(time.Millisecond)
		}
	}
	close(e.up.Block)
	for range bufs {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	e.s.wg.Wait()
	for i := range bufs {
		if !bytes.Equal(bufs[i].Bytes(), p.Body) {
			t.Fatalf("client %d got wrong bytes", i)
		}
	}
	if len(e.rows()) != 1 {
		t.Fatalf("rows %v", e.rows())
	}
}

func TestFetchCredentialsScrubbed(t *testing.T) {
	box, err := secretbox.New("k")
	if err != nil {
		t.Fatal(err)
	}
	e, _, en := refreshed(t, fakeOpts{})
	e.s.Box = box
	m := e.mirror()
	m.Username, m.TokenEnc = "bob", sealed(t, box, "s3cret-token")
	if err := e.st.SetMirror(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	e.up.Fail = true
	err = e.s.Fetch(context.Background(), e.repo, en, io.Discard)
	if err == nil || strings.Contains(err.Error(), "s3cret-token") {
		t.Fatalf("err %v", err)
	}
}
