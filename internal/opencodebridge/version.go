package opencodebridge

import "golang.org/x/mod/semver"

const MinimumNativeVersion = "1.19.3"

// SupportedNativeVersion accepts canonical stable releases from the native API floor.
func SupportedNativeVersion(version string) bool {
	v := "v" + version
	return semver.IsValid(v) && semver.Canonical(v) == v && semver.Prerelease(v) == "" && semver.Compare(v, "v"+MinimumNativeVersion) >= 0
}
