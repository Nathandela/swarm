package accounts

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNativeRetirementHoldsPendingContextBeforeAnyCredentialDeletion(t *testing.T) {
	for _, provider := range []string{ProviderCodex, ProviderClaude} {
		for _, intent := range []string{"private-file", "foreign-alias", "directory"} {
			t.Run(provider+"/"+intent, func(t *testing.T) {
				s, c, a, _, r := retiringNative(t, provider)
				marker := filepath.Join(c.ProfilePath, ".swarm-"+provider+"-context-update.json")
				credential, version := "auth.json", "0.160.0"
				if provider == ProviderClaude {
					credential, version = ".credentials.json", "2.1.289"
				}
				switch intent {
				case "private-file":
					if err := os.WriteFile(marker, []byte(`{"SchemaVersion":1,"Previous":{},"Next":{}}`), 0600); err != nil {
						t.Fatal(err)
					}
				case "foreign-alias":
					if err := os.Symlink(filepath.Join(c.ProfilePath, credential), marker); err != nil {
						t.Fatal(err)
					}
				case "directory":
					if err := os.Mkdir(marker, 0700); err != nil {
						t.Fatal(err)
					}
				}
				before, err := os.ReadFile(filepath.Join(c.ProfilePath, credential))
				if err != nil {
					t.Fatal(err)
				}
				r, err = s.BeginCredentialErasure(r.Revision, a.ID, 1, version)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := s.EraseCredentials(r.Revision, a.ID, 1, ErasureProof{WritersStopped: true}); err == nil {
					t.Fatal("pending configuration repair admitted credential deletion")
				}
				after, err := os.ReadFile(filepath.Join(c.ProfilePath, credential))
				if err != nil || string(before) != string(after) {
					t.Fatal("pending repair lost credential custody")
				}
				if _, err := os.Lstat(marker); err != nil {
					t.Fatal("pending configuration evidence removed")
				}
			})
		}
	}

}
