package upgrade

import "testing"

func TestValidateReleaseVersionAndTag_NumericOnly(t *testing.T) {
	validVersions := []string{
		"0.0.0",
		"0.26.1",
		"0.27.0",
		"1.0.0",
		"12.34.56",
	}
	for _, v := range validVersions {
		if err := ValidateReleaseVersion(v); err != nil {
			t.Errorf("ValidateReleaseVersion(%q) unexpected error: %v", v, err)
		}
		if err := ValidateReleaseTag("v" + v); err != nil {
			t.Errorf("ValidateReleaseTag(%q) unexpected error: %v", "v"+v, err)
		}
	}

	invalidVersions := []string{
		"",
		"dev",
		"v0.27.0",
		"0.27.0-prep",
		"0.27.0-rc1",
		"0.27.0-rc.1",
		"0.27.0-alpha",
		"0.27.0-SNAPSHOT-deadbeef",
		"0.27.0+build1",
		"0.27",
		"0.27.0.1",
		"+0.27.0",
		"0.+1.0",
		"0.27.-1",
		"01.27.0",
		"0.027.0",
		"0.27.00",
		" 0.27.0",
		"0.27.0 ",
	}
	for _, v := range invalidVersions {
		if err := ValidateReleaseVersion(v); err == nil {
			t.Errorf("ValidateReleaseVersion(%q) = nil, want rejection", v)
		}
	}

	invalidTags := []string{
		"",
		"0.27.0",
		"V0.27.0",
		"vv0.27.0",
		"v0.27.0-prep",
		"v0.27.0-rc1",
		"v0.27.0-beta.2",
		"v0.27.0+20261118",
		"v0.27.0-dirty",
		"v+0.27.0",
		"v01.27.0",
		"v0.027.0",
		"v0.27.00",
		"v0.27",
		"v0.27.0.0",
		" v0.27.0",
		"v0.27.0\n",
	}
	for _, tag := range invalidTags {
		if err := ValidateReleaseTag(tag); err == nil {
			t.Errorf("ValidateReleaseTag(%q) = nil, want rejection", tag)
		}
	}

	for _, bad := range []string{"+1.2.3", "01.2.3", "0.27.0-prep"} {
		if _, err := parseSemver(bad); err == nil {
			t.Errorf("parseSemver(%q) = nil, want rejection", bad)
		}
	}
}
