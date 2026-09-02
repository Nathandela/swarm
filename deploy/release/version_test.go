package release_test

import (
	"strings"
	"testing"
)

func TestOperationalExamplesNameTheCurrentRelease(t *testing.T) {
	for _, path := range []string{
		"../relay/docker-compose.yml",
		"../relay/Dockerfile",
		"../pushgw/docker-compose.yml",
		"../pushgw/pushgw.env.example",
		"../../docs/operations/relay-vps-deploy.md",
		"../../docs/operations/gcp-production-iac.md",
	} {
		contents := read(t, path)
		if !strings.Contains(contents, "v0.13.23") {
			t.Errorf("%s does not name the current immutable release v0.13.23", path)
		}
		if strings.Contains(contents, "v0.13.22") {
			t.Errorf("%s still recommends consumed release v0.13.22", path)
		}
	}
}
