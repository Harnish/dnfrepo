package mirror

import (
	"errors"
	"fmt"
	"path"
	"sort"

	"dnfrepo/internal/debver"
	"dnfrepo/internal/rpmver"
	"dnfrepo/internal/store"
)

// Filter selects which upstream packages to mirror. Globs match the package
// name (path.Match). Empty Arches/Include allow everything; noarch is always
// allowed. KeepLatest <= 0 means no filter-side limit.
type Filter struct {
	Arches, Include, Exclude []string
	KeepLatest               int
	// Compare orders two versions (version, release pairs); nil means rpm ordering.
	Compare func(verA, relA, verB, relB string) int
}

// debCompare orders deb versions; deb rows keep the whole version (epoch and revision) in Version.
func debCompare(verA, _, verB, _ string) int { return debver.Compare(verA, verB) }

var ErrConflict = errors.New("upstream file has a different checksum than the repo's file of the same name")

func anyMatch(globs []string, name string) (bool, error) {
	for _, g := range globs {
		ok, err := path.Match(g, name)
		if err != nil {
			return false, fmt.Errorf("bad glob %q: %w", g, err)
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

// Plan returns the upstream packages to download: those passing f that would
// survive retention (the smallest positive of f.KeepLatest and the repo keep,
// override per name beating the default) ranked together with what the repo
// already has, so a sync never fetches what the next purge would delete. Ranking
// mirrors pipeline.purgeLocked; a new candidate wins ties on version/release.
// A bad glob is an error. A filtered candidate whose filename exists in have
// with a different sha256 is ErrConflict, even if retention would drop it.
func Plan(up []UpstreamPkg, have []store.Package, f Filter, repoKeep int, overrides map[string]int) ([]UpstreamPkg, error) {
	haveSum := map[string]string{}
	groups := map[[2]string][]store.Package{}
	for _, p := range have {
		haveSum[p.Filename] = p.SHA256
		k := [2]string{p.Name, p.Arch}
		groups[k] = append(groups[k], p)
	}
	cmp := f.Compare
	if cmp == nil {
		cmp = rpmver.ComparePackage
	}
	var cands []UpstreamPkg
	for _, p := range up {
		if len(f.Arches) > 0 && p.Arch != "noarch" && p.Arch != "all" && !contains(f.Arches, p.Arch) {
			continue
		}
		if len(f.Include) > 0 {
			ok, err := anyMatch(f.Include, p.Name)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
		}
		if ok, err := anyMatch(f.Exclude, p.Name); err != nil {
			return nil, err
		} else if ok {
			continue
		}
		if sum, ok := haveSum[p.Filename]; ok {
			if sum != p.SHA256 {
				return nil, fmt.Errorf("%s: %w", p.Filename, ErrConflict)
			}
			continue // already present
		}
		cands = append(cands, p)
		k := [2]string{p.Name, p.Arch}
		groups[k] = append(groups[k], store.Package{Name: p.Name, Arch: p.Arch, Version: p.Version, Release: p.Release, Filename: p.Filename, SHA256: p.SHA256})
	}

	keep := map[string]bool{} // filenames inside the limit
	for k, g := range groups {
		limit := repoKeep
		if v, ok := overrides[k[0]]; ok {
			limit = v
		}
		if f.KeepLatest > 0 && (limit <= 0 || f.KeepLatest < limit) {
			limit = f.KeepLatest
		}
		if limit <= 0 || limit > len(g) {
			limit = len(g)
		}
		sort.Slice(g, func(i, j int) bool { // newest first
			if c := cmp(g[i].Version, g[i].Release, g[j].Version, g[j].Release); c != 0 {
				return c > 0
			}
			_, ih := haveSum[g[i].Filename]
			_, jh := haveSum[g[j].Filename]
			if ih != jh {
				return !ih // candidates are newer than existing rows
			}
			return g[i].Filename > g[j].Filename
		})
		for _, p := range g[:limit] {
			keep[p.Filename] = true
		}
	}
	var out []UpstreamPkg
	for _, p := range cands {
		if keep[p.Filename] {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if a.Arch != b.Arch {
			return a.Arch < b.Arch
		}
		return a.Filename < b.Filename
	})
	return out, nil
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
