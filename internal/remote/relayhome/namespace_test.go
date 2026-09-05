package relayhome_test

import (
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/relayhome"
)

func TestNamespaceIsOneCanonicalBoundedToken(t *testing.T) {
	for _, good := range []string{"owner", "owner-2", "a", "a" + strings.Repeat("9", 63)} {
		if err := relayhome.ValidateNamespace(good); err != nil {
			t.Errorf("ValidateNamespace(%q): %v", good, err)
		}
	}
	for _, bad := range []string{"", "Owner", "2owner", "owner_2", "owner ", "owner/2", "öwner", "a" + strings.Repeat("9", 64)} {
		if err := relayhome.ValidateNamespace(bad); err == nil {
			t.Errorf("ValidateNamespace(%q) succeeded", bad)
		}
	}
}
