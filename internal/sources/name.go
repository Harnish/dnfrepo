// Package sources fetches the latest rpms from GitHub releases and Plex.
package sources

import (
	"regexp"
	"strings"
)

// Release is one downloadable rpm, with the filename it should be stored under.
type Release struct{ Filename, URL string }

var hasVersion = regexp.MustCompile(`\d+\.\d+`)

// VersionedName appends the release tag before ".rpm" when the asset name has no N.N version part,
// e.g. k8sgpt_amd64.rpm from tag v0.4.1 -> k8sgpt_amd64-0.4.1.rpm.
func VersionedName(asset, tag string) string {
	v := strings.TrimPrefix(strings.TrimPrefix(tag, "v"), "V")
	if v == "" || hasVersion.MatchString(asset) {
		return asset
	}
	return asset[:len(asset)-len(".rpm")] + "-" + v + ".rpm"
}
