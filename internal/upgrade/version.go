package upgrade

import (
	"fmt"
	"strconv"
	"strings"
)

// semver is the MAJOR.MINOR.PATCH triple this repo tags releases with. No
// prerelease grammar: the release pipeline has never published one, and a
// parser for syntax nobody emits is a place for bugs to live.
type semver struct{ major, minor, patch int }

// parseSemver reads "1.2.3" or "v1.2.3". Anything else -- including the "dev"
// an unstamped local build carries -- is an error, and the caller's policy for
// an unparseable INSTALLED version is refusal: a dev build overwritten nightly
// by the latest tag was the committee's go-install finding (C4), and refusing
// to act on what cannot be compared is the whole answer.
func parseSemver(s string) (semver, error) {
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(s), "v"), ".")
	if len(parts) != 3 {
		return semver{}, fmt.Errorf("upgrade: %q is not MAJOR.MINOR.PATCH", s)
	}
	var v semver
	for i, dst := range []*int{&v.major, &v.minor, &v.patch} {
		n, err := strconv.Atoi(parts[i])
		if err != nil || n < 0 {
			return semver{}, fmt.Errorf("upgrade: %q is not MAJOR.MINOR.PATCH", s)
		}
		*dst = n
	}
	return v, nil
}

// compare returns -1, 0 or 1 as a orders before, equal to, or after b.
func (a semver) compare(b semver) int {
	for _, d := range []int{a.major - b.major, a.minor - b.minor, a.patch - b.patch} {
		if d < 0 {
			return -1
		}
		if d > 0 {
			return 1
		}
	}
	return 0
}
