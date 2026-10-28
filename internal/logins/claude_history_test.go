package logins

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// The purpose-built read seam keeps the anchored descriptor alive. It is not
// native birth/admission proof and must not reopen a returned pathname.
type claudeProjectsReader interface {
	OpenClaudeProfileProjects(Runtime) (*os.Root, error)
}

func requireClaudeProjectsReader(t *testing.T, s *Store) claudeProjectsReader {
	t.Helper()
	reader, ok := any(s).(claudeProjectsReader)
	if !ok {
		t.Fatal("native Claude profile projects descriptor API is absent; API-absence RED only")
	}
	return reader
}

func writeNativeProfileHistory(t *testing.T, s *Store) ([]byte, string) {
	t.Helper()
	dir := filepath.Join(s.Home(ProviderClaude), "projects", "-work")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	const name = "1389ef09-4c19-4d50-8fdd-1fc95bdcfd4a.jsonl"
	raw := []byte("{\"type\":\"user\",\"uuid\":\"2389ef09-4c19-4d50-8fdd-1fc95bdcfd4a\",\"sessionId\":\"1389ef09-4c19-4d50-8fdd-1fc95bdcfd4a\",\"parentUuid\":null,\"isSidechain\":false,\"message\":{\"role\":\"user\",\"content\":\"native owner\"}}\n")
	if err := os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
		t.Fatal(err)
	}
	return raw, filepath.Join("-work", name)
}

func TestClaudeNativeHistoryUsesExactProfileProjectsDescriptor(t *testing.T) {
	s, _, _, rt := claudeBirthFixture(t)
	expected, relative := writeNativeProfileHistory(t, s)
	reader := requireClaudeProjectsReader(t, s)
	root, err := reader.OpenClaudeProfileProjects(rt)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	f, err := root.Open(relative)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(f)
	if err != nil || !bytes.Equal(raw, expected) {
		t.Fatal("profile history descriptor did not reach existing shared native history", err)
	}
}

func TestClaudeNativeHistoryRefusesForeignProfileProjectsAlias(t *testing.T) {
	for _, kind := range []string{"foreign-projects-alias", "profile-owner", "runtime-home", "runtime-identity", "external-auth"} {
		t.Run(kind, func(t *testing.T) {
			s, _, _, rt := claudeBirthFixture(t)
			writeNativeProfileHistory(t, s)
			reader := requireClaudeProjectsReader(t, s)
			switch kind {
			case "foreign-projects-alias":
				alias := filepath.Join(rt.Home, "projects")
				if err := os.Remove(alias); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), alias); err != nil {
					t.Fatal(err)
				}
			case "profile-owner":
				if err := os.WriteFile(filepath.Join(rt.Home, claudeProfileOwner), []byte("wrong-owner"), 0600); err != nil {
					t.Fatal(err)
				}
			case "runtime-home":
				rt.Home = s.Home(ProviderClaude)
			case "runtime-identity":
				rt.Identity = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
			case "external-auth":
				rt.ExternalAuth = true
			}
			root, err := reader.OpenClaudeProfileProjects(rt)
			if root != nil {
				_ = root.Close()
			}
			if err == nil {
				t.Fatalf("unsafe history root accepted: %s", kind)
			}
		})
	}
}

func TestClaudeNativeHistoryHeldRootNeverReopensReplacementName(t *testing.T) {
	s, _, _, rt := claudeBirthFixture(t)
	expected, relative := writeNativeProfileHistory(t, s)
	root, err := requireClaudeProjectsReader(t, s).OpenClaudeProfileProjects(rt)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	projects := filepath.Join(s.Home(ProviderClaude), "projects")
	if err := os.Rename(projects, projects+".held"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(projects, "-work"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projects, relative), []byte("replacement, not native history\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := root.Open(relative)
	if err != nil {
		return
	} // Explicit refusal is safe; a name-based reopen is not.
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(f)
	if err != nil || !bytes.Equal(raw, expected) {
		t.Fatal("anchored history reopened replacement namespace", err)
	}
}
