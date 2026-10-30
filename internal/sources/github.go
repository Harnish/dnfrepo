package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// RepoName normalizes "https://github.com/o/n" or "o/n" to "o/n".
func RepoName(s string) string {
	return strings.Trim(strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(s), "https://"), "github.com/"), "/")
}

// LatestGitHub finds the x86_64/amd64 .rpm in the latest release of ownerName.
// apiBase is "https://api.github.com" in production.
func LatestGitHub(ctx context.Context, c *http.Client, apiBase, token, ownerName string) (Release, error) {
	repo := RepoName(ownerName)
	req, err := http.NewRequestWithContext(ctx, "GET", apiBase+"/repos/"+repo+"/releases/latest", nil)
	if err != nil {
		return Release{}, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.Do(req)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return Release{}, fmt.Errorf("github %s: %s", repo, resp.Status)
	}
	var rel struct {
		Tag    string `json:"tag_name"`
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return Release{}, err
	}
	for _, a := range rel.Assets {
		n := strings.ToLower(a.Name)
		if strings.HasSuffix(n, ".rpm") && (strings.Contains(n, "x86_64") || strings.Contains(n, "amd64")) {
			return Release{Filename: VersionedName(a.Name, rel.Tag), URL: a.URL}, nil
		}
	}
	return Release{}, fmt.Errorf("no x86_64/amd64 rpm in latest release of %s", repo)
}
