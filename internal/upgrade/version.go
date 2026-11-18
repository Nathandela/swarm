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

// ValidateReleaseVersion enforces that a release version is strictly numeric
// MAJOR.MINOR.PATCH (for example "0.27.0"), with no leading "v", whitespace,
// leading zeros on multi-digit components, signs, or suffixes such as "-prep".
func ValidateReleaseVersion(s string) error {
	_, err := parseExactNumericVersion(s)
	return err
}

// ValidateReleaseTag enforces that a release tag is strictly "v" followed by a
// numeric MAJOR.MINOR.PATCH version (for example "v0.27.0").
func ValidateReleaseTag(tag string) error {
	if !strings.HasPrefix(tag, "v") {
		return fmt.Errorf("upgrade: %q is not an exact vMAJOR.MINOR.PATCH release tag", tag)
	}
	if err := ValidateReleaseVersion(tag[1:]); err != nil {
		return fmt.Errorf("upgrade: %q is not an exact vMAJOR.MINOR.PATCH release tag", tag)
	}
	return nil
}

func parseExactNumericVersion(s string) (semver, error) {
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return semver{}, fmt.Errorf("upgrade: %q is not MAJOR.MINOR.PATCH", s)
	}
	var v semver
	for i, dst := range []*int{&v.major, &v.minor, &v.patch} {
		part := parts[i]
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return semver{}, fmt.Errorf("upgrade: %q is not MAJOR.MINOR.PATCH", s)
		}
		for j := 0; j < len(part); j++ {
			if part[j] < '0' || part[j] > '9' {
				return semver{}, fmt.Errorf("upgrade: %q is not MAJOR.MINOR.PATCH", s)
			}
		}
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return semver{}, fmt.Errorf("upgrade: %q is not MAJOR.MINOR.PATCH", s)
		}
		*dst = n
	}
	return v, nil
}

// parseSemver reads "1.2.3" or "v1.2.3". Anything else -- including the "dev"
// an unstamped local build carries -- is an error, and the caller's policy for
// an unparseable INSTALLED version is refusal: a dev build overwritten nightly
// by the latest tag was the committee's go-install finding (C4), and refusing
// to act on what cannot be compared is the whole answer.
func parseSemver(s string) (semver, error) {
	trimmed := strings.TrimPrefix(strings.TrimSpace(s), "v")
	v, err := parseExactNumericVersion(trimmed)
	if err != nil {
		return semver{}, fmt.Errorf("upgrade: %q is not MAJOR.MINOR.PATCH", s)
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
