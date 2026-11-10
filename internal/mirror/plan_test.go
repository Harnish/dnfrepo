package mirror

import (
	"errors"
	"fmt"
	"testing"

	"dnfrepo/internal/store"
)

func up(name, arch string, v int) UpstreamPkg {
	rel := "1"
	return UpstreamPkg{Name: name, Arch: arch, Version: fmt.Sprint(v), Release: rel,
		Filename: fmt.Sprintf("%s-%d-1.%s.rpm", name, v, arch), SHA256: fmt.Sprintf("sum-%s-%d", name, v)}
}

func have(u UpstreamPkg) store.Package {
	return store.Package{Name: u.Name, Arch: u.Arch, Version: u.Version, Release: u.Release, Filename: u.Filename, SHA256: u.SHA256}
}

func names(ps []UpstreamPkg) []string {
	var o []string
	for _, p := range ps {
		o = append(o, p.Filename)
	}
	return o
}

func eq(t *testing.T, got []UpstreamPkg, want ...string) {
	t.Helper()
	g := names(got)
	if fmt.Sprint(g) != fmt.Sprint(want) && !(len(g) == 0 && len(want) == 0) {
		t.Fatalf("got %v want %v", g, want)
	}
}

func TestPlanArchIncludeExclude(t *testing.T) {
	in := []UpstreamPkg{up("foo", "x86_64", 1), up("foo", "aarch64", 1), up("bar", "x86_64", 1), up("foo-debug", "x86_64", 1)}
	got, err := Plan(in, nil, Filter{Arches: []string{"x86_64"}, Include: []string{"foo*"}, Exclude: []string{"*-debug"}}, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, got, "foo-1-1.x86_64.rpm")
	if _, err := Plan(in, nil, Filter{Include: []string{"["}}, 0, nil); err == nil {
		t.Fatal("bad pattern should error")
	}
	if _, err := Plan(in, nil, Filter{Exclude: []string{"["}}, 0, nil); err == nil {
		t.Fatal("bad pattern should error")
	}
}

func TestPlanNoarchAlwaysAllowed(t *testing.T) {
	got, _ := Plan([]UpstreamPkg{up("a", "noarch", 1), up("b", "i686", 1)}, nil, Filter{Arches: []string{"x86_64"}}, 0, nil)
	eq(t, got, "a-1-1.noarch.rpm")
}

func TestPlanKeepLatestPerNameArch(t *testing.T) {
	var in []UpstreamPkg
	for v := 1; v <= 3; v++ {
		in = append(in, up("a", "x86_64", v), up("a", "aarch64", v), up("b", "x86_64", v))
	}
	got, _ := Plan(in, nil, Filter{KeepLatest: 2}, 0, nil)
	eq(t, got, "a-2-1.aarch64.rpm", "a-3-1.aarch64.rpm", "a-2-1.x86_64.rpm", "a-3-1.x86_64.rpm", "b-2-1.x86_64.rpm", "b-3-1.x86_64.rpm")
}

func TestPlanNoRedownloadAfterRetention(t *testing.T) {
	var in []UpstreamPkg
	for v := 1; v <= 5; v++ {
		in = append(in, up("a", "x86_64", v))
	}
	got, _ := Plan(in, []store.Package{have(in[2]), have(in[3]), have(in[4])}, Filter{}, 3, nil)
	eq(t, got)
	got, _ = Plan(in, nil, Filter{}, 3, nil)
	eq(t, got, "a-3-1.x86_64.rpm", "a-4-1.x86_64.rpm", "a-5-1.x86_64.rpm")
	// newer upstream arrives; repo full of older ones
	in = append(in, up("a", "x86_64", 6))
	got, _ = Plan(in, []store.Package{have(in[2]), have(in[3]), have(in[4])}, Filter{}, 3, nil)
	eq(t, got, "a-6-1.x86_64.rpm")
	// repo already holds newer-than-upstream versions: older candidates are not wanted
	got, _ = Plan(in[:2], []store.Package{have(up("a", "x86_64", 7)), have(up("a", "x86_64", 8)), have(up("a", "x86_64", 9))}, Filter{}, 3, nil)
	eq(t, got)
}

func TestPlanSkipsPresentSameChecksum(t *testing.T) {
	a, b := up("a", "x86_64", 1), up("a", "x86_64", 2)
	got, err := Plan([]UpstreamPkg{a, b}, []store.Package{have(a)}, Filter{}, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, got, b.Filename)
}

func TestPlanConflictOnDifferentChecksum(t *testing.T) {
	a := up("a", "x86_64", 1)
	h := have(a)
	h.SHA256 = "other"
	if _, err := Plan([]UpstreamPkg{a}, []store.Package{h}, Filter{}, 0, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("got %v", err)
	}
	// filtered-out candidates do not conflict
	if _, err := Plan([]UpstreamPkg{a}, []store.Package{h}, Filter{Exclude: []string{"a"}}, 0, nil); err != nil {
		t.Fatal(err)
	}
}

func TestPlanOverrideBeatsDefault(t *testing.T) {
	var in []UpstreamPkg
	for v := 1; v <= 4; v++ {
		in = append(in, up("a", "x86_64", v), up("b", "x86_64", v))
	}
	got, _ := Plan(in, nil, Filter{}, 3, map[string]int{"a": 1, "b": 0})
	eq(t, got, "a-4-1.x86_64.rpm", "b-1-1.x86_64.rpm", "b-2-1.x86_64.rpm", "b-3-1.x86_64.rpm", "b-4-1.x86_64.rpm")
}
