package mirror

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"runtime"
	"strings"
	"testing"

	"github.com/ulikunitz/xz"
)

func TestPackagesParsesPrimaryGzZstXz(t *testing.T) {
	for _, c := range []string{"gz", "zst", "xz"} {
		t.Run(c, func(t *testing.T) {
			f := newFakeUpstream(t, fakeOpts{Compression: c})
			a := f.AddPkg("hello", "1.0", "1.fc42", "x86_64", []byte("hello body"))
			f.AddPkg("world", "2.1", "3", "noarch", []byte("world body!"))
			u := &Upstream{Base: f.URL()}
			pkgs, err := u.Packages(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(pkgs) != 2 {
				t.Fatalf("got %d packages", len(pkgs))
			}
			want := UpstreamPkg{Name: "hello", Arch: "x86_64", Version: "1.0", Release: "1.fc42",
				Filename: "hello-1.0-1.fc42.x86_64.rpm", Href: a.Href, SHA256: sum(a.Body), Size: int64(len(a.Body))}
			if pkgs[0] != want {
				t.Fatalf("got %+v\nwant %+v", pkgs[0], want)
			}
		})
	}
}

func TestPackagesVerifiesMetadataChecksum(t *testing.T) {
	f := newFakeUpstream(t, fakeOpts{})
	f.AddPkg("hello", "1", "1", "x86_64", []byte("b"))
	f.TamperPrimary = true
	_, err := (&Upstream{Base: f.URL()}).Packages(context.Background())
	if !errors.Is(err, ErrChecksum) {
		t.Fatalf("want ErrChecksum, got %v", err)
	}
}

func TestSignatureRequiredAndVerified(t *testing.T) {
	ctx := context.Background()
	signed := newFakeUpstream(t, fakeOpts{Sign: true})
	signed.AddPkg("hello", "1", "1", "x86_64", []byte("b"))
	if _, err := (&Upstream{Base: signed.URL(), GPGKey: signed.PubKey, RequireSig: true}).Packages(ctx); err != nil {
		t.Fatalf("valid signature: %v", err)
	}

	signed.BadSig = true
	signed.Requests = nil
	if _, err := (&Upstream{Base: signed.URL(), GPGKey: signed.PubKey}).Packages(ctx); err == nil {
		t.Fatal("bad signature accepted")
	}
	for _, r := range signed.Requests {
		if strings.Contains(r.URL.Path, "primary") {
			t.Fatal("primary fetched despite bad signature")
		}
	}

	other := newFakeUpstream(t, fakeOpts{Sign: true}) // signed by a different key
	if _, err := (&Upstream{Base: other.URL(), GPGKey: signed.PubKey}).Packages(ctx); err == nil {
		t.Fatal("signature by wrong key accepted")
	}

	unsigned := newFakeUpstream(t, fakeOpts{})
	if _, err := (&Upstream{Base: unsigned.URL(), GPGKey: unsigned.PubKey}).Packages(ctx); err == nil {
		t.Fatal("key set but .asc absent: accepted")
	}
	if _, err := (&Upstream{Base: unsigned.URL(), RequireSig: true}).Packages(ctx); err == nil {
		t.Fatal("RequireSig without key: accepted")
	}
}

func TestBasicAuthSentWhenCredentialsSet(t *testing.T) {
	ctx := context.Background()
	f := newFakeUpstream(t, fakeOpts{Username: "bob", Password: "s3cret-token"})
	p := f.AddPkg("hello", "1", "1", "x86_64", []byte("body"))

	_, err := (&Upstream{Base: f.URL()}).Packages(ctx)
	if err == nil {
		t.Fatal("no credentials: accepted")
	}
	_, err = (&Upstream{Base: f.URL(), Username: "bob", Token: "wrong-token"}).Packages(ctx)
	if err == nil || strings.Contains(err.Error(), "wrong-token") {
		t.Fatalf("wrong credentials: %v", err)
	}

	u := &Upstream{Base: f.URL(), Username: "bob", Token: "s3cret-token"}
	pkgs, err := u.Packages(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := u.Download(ctx, pkgs[0], &buf); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf.Bytes(), p.Body) {
		t.Fatal("body mismatch")
	}
}

func TestDownloadVerifiesSizeAndSHA256(t *testing.T) {
	ctx := context.Background()
	f := newFakeUpstream(t, fakeOpts{})
	p := f.AddPkg("hello", "1", "1", "x86_64", []byte("0123456789"))
	u := &Upstream{Base: f.URL()}
	pkgs, err := u.Packages(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := u.Download(ctx, pkgs[0], &buf); err != nil || buf.String() != "0123456789" {
		t.Fatalf("good download: %v %q", err, buf.String())
	}

	f.Corrupt(p)
	if err := u.Download(ctx, pkgs[0], &bytes.Buffer{}); !errors.Is(err, ErrChecksum) {
		t.Fatalf("corrupt: want ErrChecksum, got %v", err)
	}

	f.Override[p.Href] = []byte("0123456789-and-more")
	buf.Reset()
	if err := u.Download(ctx, pkgs[0], &buf); !errors.Is(err, ErrChecksum) {
		t.Fatalf("oversize: want ErrChecksum, got %v", err)
	}
	if int64(buf.Len()) > pkgs[0].Size {
		t.Fatalf("wrote %d bytes past declared size", buf.Len())
	}

	f.Override[p.Href] = []byte("01234")
	if err := u.Download(ctx, pkgs[0], &bytes.Buffer{}); !errors.Is(err, ErrChecksum) {
		t.Fatalf("short: want ErrChecksum, got %v", err)
	}

	delete(f.Override, p.Href)
	u.MaxRPMBytes = 5
	if err := u.Download(ctx, pkgs[0], &bytes.Buffer{}); err == nil {
		t.Fatal("over MaxRPMBytes accepted")
	}

	u.MaxRPMBytes = 0
	f.Drop(p.Href)
	if err := u.Download(ctx, pkgs[0], &bytes.Buffer{}); err == nil {
		t.Fatal("404 accepted")
	}
}

func TestUnsupportedChecksumTypeRejected(t *testing.T) {
	f := newFakeUpstream(t, fakeOpts{})
	f.AddPkg("hello", "1", "1", "x86_64", []byte("b"))
	f.ChecksumType = "sha1"
	if _, err := (&Upstream{Base: f.URL()}).Packages(context.Background()); err == nil || !strings.Contains(err.Error(), "sha1") {
		t.Fatalf("want sha1 rejected, got %v", err)
	}
}

func TestHostileLocationHrefRejected(t *testing.T) {
	for _, href := range []string{
		"../../x.rpm",
		"Packages/../../x.rpm",
		"/abs.rpm",
		"http://evil/x.rpm",
		"file:x.rpm",
		"%2e%2e/x.rpm",
		`Packages\x.rpm`,
		"Packages/x.rpm?q=1",
		"Packages/-bad.rpm",
		"Packages/notanrpm.txt",
	} {
		t.Run(href, func(t *testing.T) {
			f := newFakeUpstream(t, fakeOpts{})
			f.AddPkg("good", "1", "1", "x86_64", []byte("b"))
			f.AddPkg("hello", "1", "1", "x86_64", []byte("b")).Href = href
			pkgs, err := (&Upstream{Base: f.URL()}).Packages(context.Background())
			if err == nil || pkgs != nil {
				t.Fatalf("accepted: %v %v", pkgs, err)
			}
			if !strings.Contains(err.Error(), "hello") {
				t.Fatalf("error does not name the entry: %v", err)
			}
		})
	}
}

func TestPrimaryDecompressionCapped(t *testing.T) {
	f := newFakeUpstream(t, fakeOpts{})
	for i := 0; i < 20; i++ {
		f.AddPkg("hello", "1", string(rune('a'+i)), "x86_64", []byte("b"))
	}
	u := &Upstream{Base: f.URL(), maxPrimaryBytes: 1000}
	if _, err := u.Packages(context.Background()); err == nil || !strings.Contains(err.Error(), "1000") {
		t.Fatalf("want cap error, got %v", err)
	}
}

func TestBadBaseRejected(t *testing.T) {
	if _, err := (&Upstream{Base: "ftp://h/x/"}).Packages(context.Background()); err == nil {
		t.Fatal("ftp base accepted")
	}
}

// hostileXZ returns a valid small .xz whose LZMA2 filter declares a 3 GiB
// dictionary (props byte 39), with the block header CRC32 fixed up.
func hostileXZ(t *testing.T) []byte {
	var buf bytes.Buffer
	w, err := xz.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("<metadata/>"))
	w.Close()
	b := buf.Bytes()
	hdr := 12 // stream header
	size := (int(b[hdr]) + 1) * 4
	flags := b[hdr+1]
	i := hdr + 2
	skipVarint := func() {
		for b[i]&0x80 != 0 {
			i++
		}
		i++
	}
	if flags&0x40 != 0 {
		skipVarint()
	}
	if flags&0x80 != 0 {
		skipVarint()
	}
	if b[i] != 0x21 || b[i+1] != 1 {
		t.Fatalf("unexpected filter flags % x", b[i:i+3])
	}
	b[i+2] = 39
	binary.LittleEndian.PutUint32(b[hdr+size-4:], crc32.ChecksumIEEE(b[hdr:hdr+size-4]))
	return b
}

// primaryAllocs runs Packages and returns its error and the bytes allocated.
func primaryAllocs(u *Upstream) (error, uint64) {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, err := u.Packages(context.Background())
	runtime.ReadMemStats(&after)
	return err, after.TotalAlloc - before.TotalAlloc
}

func TestHostileXZDictionaryRejected(t *testing.T) {
	f := newFakeUpstream(t, fakeOpts{Compression: "xz"})
	f.Override[f.primaryHref()] = hostileXZ(t)
	err, alloc := primaryAllocs(&Upstream{Base: f.URL(), Username: "u", Token: "tok-secret"})
	if err == nil || alloc > 256<<20 {
		t.Fatalf("err=%v alloc=%d MiB", err, alloc>>20)
	}
	if strings.Contains(err.Error(), "tok-secret") || strings.Contains(err.Error(), "u:") {
		t.Fatalf("credentials in error: %v", err)
	}
}

func TestHostileZstdWindowRejected(t *testing.T) {
	f := newFakeUpstream(t, fakeOpts{Compression: "zst"})
	// magic, FHD 0 (window descriptor present), window 2^29, raw last block of 1 byte.
	f.Override[f.primaryHref()] = []byte{0x28, 0xb5, 0x2f, 0xfd, 0x00, 0x98, 0x09, 0x00, 0x00, 'x'}
	err, alloc := primaryAllocs(&Upstream{Base: f.URL(), Username: "u", Token: "tok-secret"})
	if err == nil || alloc > 256<<20 {
		t.Fatalf("err=%v alloc=%d MiB", err, alloc>>20)
	}
	if strings.Contains(err.Error(), "tok-secret") {
		t.Fatalf("credentials in error: %v", err)
	}
}
