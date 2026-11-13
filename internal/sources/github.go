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

type ghAsset struct{ Name, URL string }

// latestRelease returns the tag and assets of the latest release of repo ("owner/name").
func latestRelease(ctx context.Context, c *http.Client, apiBase, token, repo string) (string, []ghAsset, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", apiBase+"/repos/"+repo+"/releases/latest", nil)
	if err != nil {
		return "", nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", nil, fmt.Errorf("github %s: %s", repo, resp.Status)
	}
	var rel struct {
		Tag    string `json:"tag_name"`
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", nil, err
	}
	out := make([]ghAsset, len(rel.Assets))
	for i, a := range rel.Assets {
		out[i] = ghAsset{a.Name, a.URL}
	}
	return rel.Tag, out, nil
}

// LatestGitHub finds the x86_64/amd64 .rpm in the latest release of ownerName.
// apiBase is "https://api.github.com" in production.
func LatestGitHub(ctx context.Context, c *http.Client, apiBase, token, ownerName string) (Release, error) {
	repo := RepoName(ownerName)
	tag, assets, err := latestRelease(ctx, c, apiBase, token, repo)
	if err != nil {
		return Release{}, err
	}
	for _, a := range assets {
		n := strings.ToLower(a.Name)
		if strings.HasSuffix(n, ".rpm") && (strings.Contains(n, "x86_64") || strings.Contains(n, "amd64")) {
			return Release{Filename: VersionedName(a.Name, tag), URL: a.URL}, nil
		}
	}
	return Release{}, fmt.Errorf("no x86_64/amd64 rpm in latest release of %s", repo)
}

// LatestGitHubDeb finds the .deb assets in the latest release of ownerName: the first asset for each
// of arches, then one for arch "all". An asset's arch is the last _, - or . separated word of its name
// (tool_1.2.3_linux_amd64.deb, tool-1.2.3-arm64.deb). Release.Filename is the asset name; the real
// file name comes from the deb's control data once downloaded.
func LatestGitHubDeb(ctx context.Context, c *http.Client, apiBase, token, ownerName string, arches []string) ([]Release, error) {
	repo := RepoName(ownerName)
	_, assets, err := latestRelease(ctx, c, apiBase, token, repo)
	if err != nil {
		return nil, err
	}
	var out []Release
	for _, arch := range append(append([]string{}, arches...), "all") {
		for _, a := range assets {
			stem, ok := strings.CutSuffix(strings.ToLower(a.Name), ".deb")
			if ok && len(stem) > len(arch) && strings.HasSuffix(stem, arch) && strings.ContainsRune("_-.", rune(stem[len(stem)-len(arch)-1])) {
				out = append(out, Release{Filename: a.Name, URL: a.URL})
				break
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no .deb for %s (or all) in latest release of %s", strings.Join(arches, ", "), repo)
	}
	return out, nil
}
