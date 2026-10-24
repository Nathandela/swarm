package codexnative

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func npmFixture(t *testing.T) (root, wrapper, native string) {
	t.Helper()
	platform, triple, ok := npmPlatform()
	if !ok {
		t.Skip("npm native platform is not qualified")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	wrapper = filepath.Join(root, "bin", "codex.js")
	packageRoot := filepath.Join(root, "node_modules", "@openai", "codex-"+platform)
	native = filepath.Join(packageRoot, "vendor", triple, "bin", "codex")
	for _, dir := range []string{filepath.Dir(wrapper), filepath.Dir(native)} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile("testdata/npm-codex.js")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wrapper, data, 0700); err != nil {
		t.Fatal(err)
	}
	writeJSON := func(path string, value any) {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeJSON(filepath.Join(root, "package.json"), map[string]any{"name": "@openai/codex", "version": "0.163.0", "bin": map[string]string{"codex": "bin/codex.js"}, "optionalDependencies": map[string]string{"@openai/codex-" + platform: "npm:@openai/codex@0.163.0-" + platform}})
	cpu := map[string]string{"amd64": "x64", "arm64": "arm64"}[runtime.GOARCH]
	writeJSON(filepath.Join(packageRoot, "package.json"), map[string]any{"name": "@openai/codex", "version": "0.163.0-" + platform, "os": []string{runtime.GOOS}, "cpu": []string{cpu}})
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.Close() }()
	output, err := os.OpenFile(native, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil || closeErr != nil {
		t.Fatalf("native executable fixture: %v %v", copyErr, closeErr)
	}
	return root, wrapper, native
}

func TestResolveOfficialNPMInstallation(t *testing.T) {
	root, wrapper, native := npmFixture(t)
	for _, entry := range []string{wrapper, native} {
		installation, err := Resolve(entry)
		if err != nil || installation.Path != native || installation.Version != "0.163.0" {
			t.Fatalf("official npm target resolution: %+v %v", installation, err)
		}
		if strings.Join(installation.Environment, "\n") != "CODEX_MANAGED_PACKAGE_ROOT="+root+"\nCODEX_MANAGED_BY_NPM=1" {
			t.Fatalf("verified management provenance missing: %v", installation.Environment)
		}
		poisoned := []string{"HOME=/ordinary", "CODEX_MANAGED_PACKAGE_ROOT=/untrusted", "CODEX_MANAGED_BY_BUN=1", "CODEX_MANAGED_BY_NPM=bad"}
		if got := installation.ApplyEnvironment(poisoned); strings.Join(got, "\n") != "HOME=/ordinary\n"+strings.Join(installation.Environment, "\n") {
			t.Fatalf("inherited management selectors survived: %v", got)
		}
	}
	alias := filepath.Join(root, "codex")
	if err := os.Symlink(wrapper, alias); err != nil {
		t.Fatal(err)
	}
	if installation, err := Resolve(alias); err != nil || installation.Path != native {
		t.Fatalf("npm entrypoint alias refused: %+v %v", installation, err)
	}
}

func TestResolveRejectsUnqualifiedInstallations(t *testing.T) {
	for _, tc := range []string{"wrapper", "name", "version", "dependency", "duplicate", "authority_alias", "native_version", "native_cpu", "native_os", "native_script", "native_alias", "writable_manifest", "group_writable_manifest", "group_writable_native_manifest", "group_writable_payload", "oversized_manifest", "pnpm_metadata", "vite_metadata"} {
		t.Run(tc, func(t *testing.T) {
			root, wrapper, native := npmFixture(t)
			mainManifest := filepath.Join(root, "package.json")
			nativeManifest := filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(native)))), "package.json")
			path, old, next := mainManifest, "", ""
			switch tc {
			case "wrapper":
				path, old, next = wrapper, "// Unified entry point", "// Modified entry point"
			case "name":
				old, next = `"name":"@openai/codex"`, `"name":"other"`
			case "version":
				old, next = `"version":"0.163.0"`, `"version":"0.165.0"`
			case "dependency":
				old, next = "npm:@openai/codex@0.163.0-", "npm:@openai/codex@0.162.0-"
			case "duplicate":
				old, next = `"version":"0.163.0"`, `"version":"0.162.0","version":"0.163.0"`
			case "authority_alias":
				old, next = `"version":"0.163.0"`, `"Version":"0.163.0","version":"0.163.0"`
			case "native_version":
				path, old, next = nativeManifest, "0.163.0-", "0.162.0-"
			case "native_cpu":
				path, old, next = nativeManifest, `"cpu":[`, `"cpus":[`
			case "native_os":
				path, old, next = nativeManifest, `"os":[`, `"platforms":[`
			case "native_script":
				if err := os.WriteFile(native, []byte("#!/bin/sh\necho 'codex-cli 0.163.0'\n"), 0700); err != nil {
					t.Fatal(err)
				}
			case "native_alias":
				if err := os.Rename(native, native+"-other"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(native+"-other", native); err != nil {
					t.Fatal(err)
				}
			case "writable_manifest":
				if err := os.Chmod(mainManifest, 0666); err != nil {
					t.Fatal(err)
				}
			case "group_writable_manifest":
				if err := os.Chmod(mainManifest, 0664); err != nil {
					t.Fatal(err)
				}
			case "group_writable_native_manifest":
				if err := os.Chmod(nativeManifest, 0664); err != nil {
					t.Fatal(err)
				}
			case "group_writable_payload":
				if err := os.Chmod(native, 0775); err != nil {
					t.Fatal(err)
				}
			case "oversized_manifest":
				if err := os.WriteFile(mainManifest, []byte(strings.Repeat(" ", 64<<10+1)), 0600); err != nil {
					t.Fatal(err)
				}
			case "pnpm_metadata", "vite_metadata":
				marker := filepath.Join(root, "node_modules", ".modules.yaml")
				if tc == "vite_metadata" {
					marker = filepath.Join(root, "@openai", "codex.json")
				}
				if err := os.MkdirAll(filepath.Dir(marker), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(marker, nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if old != "" {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				changed := strings.Replace(string(data), old, next, 1)
				if changed == string(data) {
					t.Fatal("negative fixture did not change")
				}
				if err := os.WriteFile(path, []byte(changed), 0600); err != nil {
					t.Fatal(err)
				}
			}
			installation, err := Resolve(wrapper)
			if err == nil {
				t.Fatalf("unqualified npm layout accepted: %+v", installation)
			}
			if (tc == "writable_manifest" || strings.HasPrefix(tc, "group_writable_")) && !errors.Is(err, ErrUnsafe) {
				t.Fatalf("unsafe official npm installation lost actionable classification: %v", err)
			}
		})
	}
}

func TestResolveDirectNativeAndUnknownWrapper(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatal(err)
	}
	if installation, err := Resolve(executable); err != nil || installation.Path != canonical || installation.Version != "" || len(installation.Environment) != 0 {
		t.Fatalf("direct native resolution: %+v %v", installation, err)
	}
	wrapper := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\necho 'codex-cli 0.163.0'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if installation, err := Resolve(wrapper); err == nil {
		t.Fatalf("version-only wrapper qualified: %+v", installation)
	}
	claimed := filepath.Join(t.TempDir(), "bin", "codex.js")
	if err := os.Mkdir(filepath.Dir(claimed), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(claimed, []byte("unrecognized source"), 0775); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(claimed); !errors.Is(err, ErrUnrecognized) || errors.Is(err, ErrUnsafe) {
		t.Fatalf("unknown source was classified as an unsafe official installation: %v", err)
	}
}
