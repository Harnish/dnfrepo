// Package rpmmeta reads package identity from an rpm header.
package rpmmeta

import (
	"errors"
	"os"

	"github.com/sassoftware/go-rpmutils"
)

var ErrNotRPM = errors.New("not a valid rpm file")

type Meta struct{ Name, Version, Release, Arch string }

func Parse(path string) (Meta, error) {
	f, err := os.Open(path)
	if err != nil {
		return Meta{}, err
	}
	defer f.Close()
	rpm, err := rpmutils.ReadRpm(f)
	if err != nil {
		return Meta{}, ErrNotRPM
	}
	var m Meta
	for _, x := range []struct {
		dst *string
		tag int
	}{{&m.Name, rpmutils.NAME}, {&m.Version, rpmutils.VERSION}, {&m.Release, rpmutils.RELEASE}, {&m.Arch, rpmutils.ARCH}} {
		if *x.dst, err = rpm.Header.GetString(x.tag); err != nil {
			return Meta{}, ErrNotRPM
		}
	}
	return m, nil
}
