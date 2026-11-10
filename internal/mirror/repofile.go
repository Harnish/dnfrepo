// Package mirror turns pasted .repo files / URLs into concrete upstream base URLs.
package mirror

import (
	"bufio"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
)

type RepoSection struct {
	ID, Name, BaseURL, Metalink, MirrorList, GPGKey string
	RepoGPGCheck, Enabled                           bool
	Username, Password                              string
}

// ParseRepoFile parses a dnf .repo INI file. Sections without a
// baseurl/metalink/mirrorlist are skipped; it is an error if none remain.
// Unknown keys are ignored. A multi-line baseurl keeps its first URL.
func ParseRepoFile(text string) ([]RepoSection, error) {
	var all []RepoSection
	var cur *RepoSection
	lastKey := ""
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		raw := sc.Text()
		line := strings.TrimSpace(raw)
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if line[0] == '[' && line[len(line)-1] == ']' {
			all = append(all, RepoSection{ID: line[1 : len(line)-1], Enabled: true})
			cur, lastKey = &all[len(all)-1], ""
			continue
		}
		if cur == nil {
			continue
		}
		if raw[0] == ' ' || raw[0] == '\t' { // continuation line
			if lastKey == "baseurl" && cur.BaseURL == "" {
				cur.BaseURL = line
			}
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
		lastKey = k
		switch k {
		case "name":
			cur.Name = v
		case "baseurl":
			if f := strings.Fields(strings.ReplaceAll(v, ",", " ")); len(f) > 0 {
				cur.BaseURL = f[0]
			}
		case "metalink":
			cur.Metalink = v
		case "mirrorlist":
			cur.MirrorList = v
		case "gpgkey":
			cur.GPGKey = v
		case "repo_gpgcheck":
			cur.RepoGPGCheck = v == "1" || strings.EqualFold(v, "true")
		case "enabled":
			cur.Enabled = !(v == "0" || strings.EqualFold(v, "false"))
		case "username":
			cur.Username = v
		case "password":
			cur.Password = v
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	var out []RepoSection
	for _, s := range all {
		if s.BaseURL != "" || s.Metalink != "" || s.MirrorList != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no repo section has a baseurl, metalink or mirrorlist")
	}
	return out, nil
}

// $$ is matched first so it is never read as a variable.
var varRe = regexp.MustCompile(`\$\$|\$\{(\w+)\}|\$(\w+)`)

func varName(m []string) string {
	if m[1] != "" {
		return m[1]
	}
	return m[2]
}

// Variables returns the distinct $name / ${name} used in s, sorted.
func Variables(s string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, m := range varRe.FindAllStringSubmatch(s, -1) {
		if n := varName(m); n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// ResolveVars substitutes known variables; unknown ones are left in place and
// reported (sorted, distinct) in the second result. $$ is left untouched.
func ResolveVars(s string, vars map[string]string) (string, []string) {
	missing := map[string]bool{}
	out := varRe.ReplaceAllStringFunc(s, func(tok string) string {
		n := varName(varRe.FindStringSubmatch(tok))
		if n == "" {
			return tok
		}
		if v, ok := vars[n]; ok {
			return v
		}
		missing[n] = true
		return tok
	})
	m := make([]string, 0, len(missing))
	for n := range missing {
		m = append(m, n)
	}
	sort.Strings(m)
	return out, m
}

const maxBody = 1 << 20

func fetch(ctx context.Context, c *http.Client, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: status %d", u, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxBody {
		return nil, fmt.Errorf("GET %s: response over %d bytes", u, maxBody)
	}
	return b, nil
}

const repomdSuffix = "repodata/repomd.xml"

// ResolveMetalink returns the first <url> ending in "repodata/repomd.xml" with
// that suffix removed, so the result keeps its trailing slash
// ("https://h/os/repodata/repomd.xml" -> "https://h/os/").
func ResolveMetalink(ctx context.Context, c *http.Client, metalinkURL string) (string, error) {
	b, err := fetch(ctx, c, metalinkURL)
	if err != nil {
		return "", err
	}
	var doc struct {
		URLs []string `xml:"files>file>resources>url"`
	}
	if err := xml.Unmarshal(b, &doc); err != nil {
		return "", fmt.Errorf("parse metalink: %w", err)
	}
	for _, u := range doc.URLs {
		if u = strings.TrimSpace(u); strings.HasSuffix(u, repomdSuffix) {
			return strings.TrimSuffix(u, repomdSuffix), nil
		}
	}
	return "", errors.New("metalink has no repodata/repomd.xml url")
}

// ResolveMirrorList returns the first non-comment, non-empty line.
func ResolveMirrorList(ctx context.Context, c *http.Client, listURL string) (string, error) {
	b, err := fetch(ctx, c, listURL)
	if err != nil {
		return "", err
	}
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" && l[0] != '#' {
			return l, nil
		}
	}
	return "", errors.New("mirrorlist is empty")
}
