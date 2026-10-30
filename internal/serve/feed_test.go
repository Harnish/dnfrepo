package serve

import (
	"context"
	"encoding/xml"
	"fmt"
	"strings"
	"testing"
)

type parsedRSS struct {
	XMLName xml.Name `xml:"rss"`
	Version string   `xml:"version,attr"`
	Channel struct {
		Title string `xml:"title"`
		Link  string `xml:"link"`
		Items []struct {
			Title   string `xml:"title"`
			Link    string `xml:"link"`
			GUID    string `xml:"guid"`
			PubDate string `xml:"pubDate"`
		} `xml:"item"`
	} `xml:"channel"`
}

func fetchFeed(t *testing.T, f *fixture) (parsedRSS, string) {
	t.Helper()
	w := f.get("/r/pub/feed.xml", "1.1.1.1:1", nil)
	if w.Code != 200 {
		t.Fatalf("feed: %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/rss+xml") {
		t.Fatalf("content-type=%q", ct)
	}
	var d parsedRSS
	if err := xml.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatalf("invalid xml: %v\n%s", err, w.Body)
	}
	return d, w.Body.String()
}

func TestFeedValidRSSAndOrdered(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	r, _ := f.st.RepoBySlug(ctx, "pub")
	f.st.AddAudit(ctx, r.ID, "admin@x", "uploaded", "old-1.rpm")
	f.st.AddAudit(ctx, r.ID, "source:github:o/n", "uploaded", "new-2.rpm")
	d, _ := fetchFeed(t, f)
	if d.Version != "2.0" || d.Channel.Title != "Repo pub" || d.Channel.Link != "https://repo.example.org/r/pub/" {
		t.Fatalf("channel=%+v", d.Channel)
	}
	if len(d.Channel.Items) != 2 {
		t.Fatalf("items=%d", len(d.Channel.Items))
	}
	if !strings.Contains(d.Channel.Items[0].Title, "new-2.rpm") || !strings.Contains(d.Channel.Items[0].Title, "github:o/n") {
		t.Fatalf("newest first / source label: %q", d.Channel.Items[0].Title)
	}
	if d.Channel.Items[0].GUID == d.Channel.Items[1].GUID || d.Channel.Items[0].PubDate == "" {
		t.Fatalf("guid/pubDate: %+v", d.Channel.Items)
	}
}

func TestFeedIncludesPromotionAndDelete(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	r, _ := f.st.RepoBySlug(ctx, "pub")
	f.st.AddAudit(ctx, r.ID, "admin@x", "promoted-in", "foo-1.rpm from dev")
	f.st.AddAudit(ctx, r.ID, "admin@x", "deleted", "bar-2.rpm")
	d, _ := fetchFeed(t, f)
	if len(d.Channel.Items) != 2 {
		t.Fatalf("items=%d", len(d.Channel.Items))
	}
	if got := d.Channel.Items[1].Title; got != "foo-1.rpm added (promoted from dev)" {
		t.Fatalf("promotion title=%q", got)
	}
	if got := d.Channel.Items[0].Title; got != "bar-2.rpm removed" {
		t.Fatalf("delete title=%q", got)
	}
}

func TestFeedCappedAt50(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	r, _ := f.st.RepoBySlug(ctx, "pub")
	for i := 0; i < 60; i++ {
		f.st.AddAudit(ctx, r.ID, "a", "uploaded", fmt.Sprintf("p-%d.rpm", i))
	}
	d, _ := fetchFeed(t, f)
	if len(d.Channel.Items) != 50 || !strings.Contains(d.Channel.Items[0].Title, "p-59.rpm") {
		t.Fatalf("items=%d first=%q", len(d.Channel.Items), d.Channel.Items[0].Title)
	}
}

func TestFeedEmptyRepoValid(t *testing.T) {
	f := newFixture(t)
	d, _ := fetchFeed(t, f)
	if len(d.Channel.Items) != 0 || d.Channel.Title == "" {
		t.Fatalf("%+v", d.Channel)
	}
}

func TestFeedEscapesMarkup(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	r, _ := f.st.RepoBySlug(ctx, "pub")
	f.st.AddAudit(ctx, r.ID, "a", "uploaded", "x<script>&.rpm")
	_, raw := fetchFeed(t, f)
	if strings.Contains(raw, "<script>") {
		t.Fatal("unescaped markup in feed")
	}
}
