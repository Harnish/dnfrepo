package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
)

const PlexURL = "https://plex.tv/api/downloads/1.json"

// LatestPlex finds the Plex Media Server redhat linux-x86_64 rpm.
func LatestPlex(ctx context.Context, c *http.Client, url string) (Release, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return Release{}, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return Release{}, fmt.Errorf("plex: %s", resp.Status)
	}
	var doc struct {
		Computer struct {
			Linux struct {
				Releases []struct{ Build, Distro, URL string }
			}
		}
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return Release{}, err
	}
	for _, r := range doc.Computer.Linux.Releases {
		if r.Distro == "redhat" && r.Build == "linux-x86_64" {
			return Release{Filename: path.Base(r.URL), URL: r.URL}, nil
		}
	}
	return Release{}, fmt.Errorf("plex: no redhat linux-x86_64 release found")
}
