package logins

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func authAliasFixture(t *testing.T, provider string) (string, string, string, string) {
	t.Helper()
	root, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	home, target, cwd := filepath.Join(root, "home"), filepath.Join(root, "data", "native"), filepath.Join(root, "home", "project")
	for _, dir := range []string{cwd, target} {
		if e = os.MkdirAll(dir, 0700); e != nil {
			t.Fatal(e)
		}
	}
	native, name, body := ".claude", "settings.json", `{"model":"haiku","permissions":{"allow":[]}}`
	if provider == ProviderCodex {
		native, name, body = ".codex", "config.toml", "model = \"gpt-6-luna\"\ncli_auth_credentials_store = \"file\"\n"
	}
	if e = os.WriteFile(filepath.Join(target, name), []byte(body), 0600); e != nil {
		t.Fatal(e)
	}
	alias := filepath.Join(home, native)
	if e = os.Symlink(target, alias); e != nil {
		t.Fatal(e)
	}
	return home, target, cwd, name
}
func TestProjectAuthOwnerNativeHomeAlias(t *testing.T) {
	for _, provider := range []string{ProviderClaude, ProviderCodex} {
		t.Run(provider, func(t *testing.T) {
			home, _, cwd, _ := authAliasFixture(t, provider)
			if e := ValidateProjectAuth(provider, cwd); !errors.Is(e, ErrUnsafePath) {
				t.Fatalf("strict old API changed: %v", e)
			}
			if e := ValidateProjectAuthEnv(provider, cwd, []string{"HOME=" + home}); e != nil {
				t.Fatalf("benign owner native alias refused: %v", e)
			}
			for _, env := range [][]string{nil, {"HOME=" + filepath.Dir(home)}, {"HOME=" + home, "HOME=" + home}, {"HOME="}, {"HOME=relative"}, {"HOME=" + home + "/../home"}} {
				if e := ValidateProjectAuthEnv(provider, cwd, env); e == nil {
					t.Fatal("missing/foreign/ambiguous HOME authorized alias")
				}
			}
		})
	}
}
func TestProjectAuthNativeAliasStillChecksAllAuthenticationSettings(t *testing.T) {
	for _, provider := range []string{ProviderClaude, ProviderCodex} {
		t.Run(provider, func(t *testing.T) {
			home, target, cwd, name := authAliasFixture(t, provider)
			body := `{"apiKeyHelper":"foreign-auth"}`
			if provider == ProviderCodex {
				body = "[profiles.external]\ncli_auth_credentials_store = \"keyring\"\n"
			} else {
				name = "settings.local.json"
			}
			if e := os.WriteFile(filepath.Join(target, name), []byte(body), 0600); e != nil {
				t.Fatal(e)
			}
			if e := ValidateProjectAuthEnv(provider, cwd, []string{"HOME=" + home}); !errors.Is(e, ErrIneligible) {
				t.Fatalf("aliased auth selector accepted: %v", e)
			}
		})
	}
}
func TestProjectAuthNativeAliasRetainsCustodyBoundaries(t *testing.T) {
	for _, provider := range []string{ProviderClaude, ProviderCodex} {
		for _, kind := range []string{"target-writable", "home-writable", "file-writable", "file-link", "file-hardlink", "project-alias", "target-missing"} {
			t.Run(provider+"/"+kind, func(t *testing.T) {
				home, target, cwd, name := authAliasFixture(t, provider)
				path := filepath.Join(target, name)
				var e error
				switch kind {
				case "target-writable":
					e = os.Chmod(target, 0770)
				case "home-writable":
					e = os.Chmod(home, 0770)
				case "file-writable":
					e = os.Chmod(path, 0660)
				case "file-link":
					e = os.Rename(path, path+".original")
					if e == nil {
						e = os.Symlink(path+".original", path)
					}
				case "file-hardlink":
					e = os.Link(path, path+".other")
				case "project-alias":
					native := ".claude"
					if provider == ProviderCodex {
						native = ".codex"
					}
					e = os.Symlink(target, filepath.Join(cwd, native))
				case "target-missing":
					e = os.Rename(target, target+".moved")
				}
				if e != nil {
					t.Fatal(e)
				}
				if e = ValidateProjectAuthEnv(provider, cwd, []string{"HOME=" + home}); !errors.Is(e, ErrUnsafePath) {
					t.Fatalf("unsafe alias custody accepted: %v", e)
				}
			})
		}
	}
}
func TestNativeAliasAndFileSwapRefused(t *testing.T) {
	for _, kind := range []string{"alias", "target", "home", "file"} {
		t.Run(kind, func(t *testing.T) {
			home, target, _, name := authAliasFixture(t, ProviderClaude)
			alias := filepath.Join(home, ".claude")
			aliasInfo, e := os.Lstat(alias)
			if e != nil {
				t.Fatal(e)
			}
			targetInfo, e := os.Lstat(target)
			if e != nil {
				t.Fatal(e)
			}
			if kind == "file" {
				root, e := os.OpenRoot(target)
				if e != nil {
					t.Fatal(e)
				}
				defer func() { _ = root.Close() }()
				before, e := root.Lstat(name)
				if e != nil {
					t.Fatal(e)
				}
				if e = os.Rename(filepath.Join(target, name), filepath.Join(target, name+".old")); e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(filepath.Join(target, name), []byte(`{}`), 0600); e != nil {
					t.Fatal(e)
				}
				if _, e = readAuthConfigFile(root, name, before, true); !errors.Is(e, ErrUnsafePath) {
					t.Fatal("swapped config accepted")
				}
				return
			}
			switch kind {
			case "alias":
				if e = os.Rename(alias, alias+".old"); e == nil {
					e = os.Symlink(target, alias)
				}
			case "target":
				if e = os.Rename(target, target+".old"); e == nil {
					e = os.Mkdir(target, 0700)
				}
			case "home":
				e = os.Chmod(home, 0770)
			}
			if e != nil {
				t.Fatal(e)
			}
			if nativeAliasUnchanged(alias, aliasInfo, target, targetInfo) {
				t.Fatal("changed alias/target custody accepted")
			}
		})
	}
}

type foreignAuthInfo struct{ os.FileInfo }

func (i foreignAuthInfo) Sys() any { st := *i.FileInfo.Sys().(*syscall.Stat_t); st.Uid++; return &st }
func TestNativeAliasForeignOwnershipRefused(t *testing.T) {
	home, target, _, name := authAliasFixture(t, ProviderClaude)
	for _, path := range []string{home, target, filepath.Join(target, name), filepath.Join(home, ".claude")} {
		info, e := os.Lstat(path)
		if e != nil {
			t.Fatal(e)
		}
		if nativeOwned(foreignAuthInfo{info}, info.IsDir()) || nativeLinkOwned(foreignAuthInfo{info}) {
			t.Fatal("foreign owner accepted")
		}
	}
}
