package serve

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"
	"time"

	"dnfrepo/internal/store"
)

type rssItem struct {
	Title   string  `xml:"title"`
	Link    string  `xml:"link"`
	GUID    rssGUID `xml:"guid"`
	PubDate string  `xml:"pubDate"`
}

type rssGUID struct {
	IsPermaLink bool   `xml:"isPermaLink,attr"`
	Value       string `xml:",chardata"`
}

type rssDoc struct {
	XMLName xml.Name `xml:"rss"`
	Version string   `xml:"version,attr"`
	Channel struct {
		Title       string    `xml:"title"`
		Link        string    `xml:"link"`
		Description string    `xml:"description"`
		Items       []rssItem `xml:"item"`
	} `xml:"channel"`
}

func eventTitle(e store.AuditEvent) string {
	switch e.Action {
	case "promoted-in":
		file, from, _ := strings.Cut(e.Detail, " from ")
		return fmt.Sprintf("%s added (promoted from %s)", file, from)
	case "deleted":
		return e.Detail + " removed"
	default: // uploaded
		if src, ok := strings.CutPrefix(e.Actor, "source:"); ok {
			return fmt.Sprintf("%s added (fetched from %s)", e.Detail, src)
		}
		return e.Detail + " added (uploaded)"
	}
}

// feed writes an RSS 2.0 feed of the repo's 50 newest audit events.
func (d Deps) feed(w http.ResponseWriter, r *http.Request, repo store.Repo) {
	events, err := d.Store.ListAudit(r.Context(), repo.ID, 50)
	if err != nil {
		http.Error(w, "internal error", 500)
		return
	}
	link := fmt.Sprintf("%s/r/%s/", d.BaseURL, repo.Slug)
	var doc rssDoc
	doc.Version = "2.0"
	doc.Channel.Title = repo.Name
	doc.Channel.Link = link
	doc.Channel.Description = "Package changes in " + repo.Name
	doc.Channel.Items = []rssItem{}
	for _, e := range events {
		doc.Channel.Items = append(doc.Channel.Items, rssItem{
			Title:   eventTitle(e),
			Link:    link,
			GUID:    rssGUID{Value: fmt.Sprintf("%s#event-%d", link, e.ID)},
			PubDate: e.At.UTC().Format(time.RFC1123Z),
		})
	}
	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	w.Write([]byte(xml.Header))
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	enc.Encode(doc)
}
