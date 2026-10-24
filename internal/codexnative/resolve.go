// Package codexnative resolves the qualified official npm launcher to its actual
// native executable. Unrecognized wrappers are not advanced native capabilities.
package codexnative

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

// Official 0.163.0/0.164.0: https://github.com/openai/codex/blob/6aaac694803004a63c6f393d153c96d22521a6b9/codex-cli/bin/codex.js
const npmLauncherSHA256 = "61b0194f3bb6534439c8d26a3ed57d0805f84b884588b761795323eeb92fcf70"

var ErrUnrecognized = errors.New("codex native installation is not qualified")
var ErrUnsafe = errors.New("unsafe Codex installation; reinstall in an owner-controlled directory without group/other write permissions")

type Installation struct {
	Path, Version string
	Environment   []string
}

// ApplyEnvironment replaces inherited management hints with verified provenance.
// Call this after the caller's ordinary environment filtering.
func (installation Installation) ApplyEnvironment(env []string) []string {
	out := make([]string, 0, len(env)+len(installation.Environment))
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(key, "CODEX_MANAGED_") {
			out = append(out, entry)
		}
	}
	return append(out, installation.Environment...)
}

// Resolve does not run a wrapper. Callers must separately require the native
// --version banner to equal Version when the recognized npm manifest supplies it.
func Resolve(path string) (Installation, error) {
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil || !filepath.IsAbs(canonical) {
		return Installation{}, ErrUnrecognized
	}
	if filepath.Base(canonical) == "codex.js" && filepath.Base(filepath.Dir(canonical)) == "bin" {
		return resolveNPM(filepath.Dir(filepath.Dir(canonical)))
	}
	if platform, triple, ok := npmPlatform(); ok {
		suffix := filepath.Join("node_modules", "@openai", "codex-"+platform, "vendor", triple, "bin", "codex")
		if strings.HasSuffix(canonical, string(filepath.Separator)+suffix) {
			root := strings.TrimSuffix(canonical, string(filepath.Separator)+suffix)
			installation, err := resolveNPM(root)
			if err != nil {
				return Installation{}, err
			}
			if installation.Path != canonical {
				return Installation{}, ErrUnrecognized
			}
			return installation, nil
		}
	}
	if err := nativeExecutable(canonical); err != nil {
		return Installation{}, err
	}
	return Installation{Path: canonical}, nil
}

func npmPlatform() (platform, triple string, ok bool) {
	arch := map[string]string{"amd64": "x64", "arm64": "arm64"}[runtime.GOARCH]
	if arch == "" {
		return "", "", false
	}
	cpu := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[runtime.GOARCH]
	switch runtime.GOOS {
	case "linux":
		return "linux-" + arch, cpu + "-unknown-linux-musl", true
	case "darwin":
		return "darwin-" + arch, cpu + "-apple-darwin", true
	}
	return "", "", false
}

func resolveNPM(root string) (Installation, error) {
	platform, triple, ok := npmPlatform()
	if !ok || strings.Contains(filepath.ToSlash(root), ".bun/install/global") {
		return Installation{}, ErrUnrecognized
	}
	for current := root; ; current = filepath.Dir(current) {
		for _, marker := range []string{filepath.Join(current, "node_modules", ".modules.yaml"), filepath.Join(current, "@openai", "codex.json")} {
			if _, err := os.Lstat(marker); err == nil {
				return Installation{}, ErrUnrecognized
			}
		}
		if filepath.Dir(current) == current {
			break
		}
	}
	wrapper, err := readSmallFile(filepath.Join(root, "bin", "codex.js"))
	digest := sha256.Sum256(wrapper)
	if hex.EncodeToString(digest[:]) != npmLauncherSHA256 {
		return Installation{}, ErrUnrecognized
	}
	if err != nil {
		return Installation{}, err
	}
	var main struct {
		Name, Version        string
		Bin                  map[string]string
		OptionalDependencies map[string]string
	}
	if err := readManifest(filepath.Join(root, "package.json"), &main); err != nil {
		return Installation{}, err
	}
	if main.Name != "@openai/codex" || main.Bin["codex"] != "bin/codex.js" {
		return Installation{}, ErrUnrecognized
	}
	packageRoot := filepath.Join(root, "node_modules", "@openai", "codex-"+platform)
	var native struct {
		Name, Version string
		OS, CPU       []string
	}
	if err := readManifest(filepath.Join(packageRoot, "package.json"), &native); err != nil {
		return Installation{}, err
	}
	path := filepath.Join(packageRoot, "vendor", triple, "bin", "codex")
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil || canonical != path {
		return Installation{}, ErrUnrecognized
	}
	if err := nativeExecutable(path); err != nil {
		return Installation{}, err
	}
	if (main.Version != "0.163.0" && main.Version != "0.164.0") || main.OptionalDependencies["@openai/codex-"+platform] != "npm:@openai/codex@"+main.Version+"-"+platform || native.Name != "@openai/codex" || native.Version != main.Version+"-"+platform || len(native.OS) != 1 || native.OS[0] != runtime.GOOS || len(native.CPU) != 1 || native.CPU[0] != strings.TrimPrefix(platform, runtime.GOOS+"-") {
		return Installation{}, ErrUnrecognized
	}
	return Installation{Path: path, Version: main.Version, Environment: []string{"CODEX_MANAGED_PACKAGE_ROOT=" + root, "CODEX_MANAGED_BY_NPM=1"}}, nil
}

func safeFile(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.Mode().IsRegular() && info.Mode().Perm()&0022 == 0 && (stat.Uid == 0 || stat.Uid == uint32(os.Getuid()))
}

func nativeExecutable(path string) error {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return ErrUnrecognized
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	var magic [4]byte
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return ErrUnrecognized
	}
	if _, err = io.ReadFull(file, magic[:]); err != nil {
		return ErrUnrecognized
	}
	valid := runtime.GOOS == "linux" && string(magic[:]) == "\x7fELF" || runtime.GOOS == "darwin" && (string(magic[:]) == "\xcf\xfa\xed\xfe" || string(magic[:]) == "\xfe\xed\xfa\xcf" || string(magic[:]) == "\xca\xfe\xba\xbe" || string(magic[:]) == "\xbe\xba\xfe\xca")
	if !valid {
		return ErrUnrecognized
	}
	if !safeFile(info) {
		return ErrUnsafe
	}
	return nil
}

func readSmallFile(path string) ([]byte, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Size() > 64<<10 {
		return nil, ErrUnrecognized
	}
	data, err := io.ReadAll(io.LimitReader(file, 64<<10+1))
	after, statErr := file.Stat()
	current, pathErr := os.Lstat(path)
	if err != nil || statErr != nil || pathErr != nil || len(data) > 64<<10 || !os.SameFile(before, current) || before.Size() != after.Size() || before.ModTime() != after.ModTime() || before.Mode() != after.Mode() {
		return nil, ErrUnrecognized
	}
	if !safeFile(before) {
		// The wrapper caller must identify these bounded bytes before classifying them.
		return data, ErrUnsafe
	}
	return data, nil
}

func readManifest(path string, destination any) error {
	data, err := readSmallFile(path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if !uniqueJSON(decoder, 0) {
		return ErrUnrecognized
	}
	if _, err = decoder.Token(); err != io.EOF {
		return ErrUnrecognized
	}
	if err := json.Unmarshal(data, destination); err != nil {
		return ErrUnrecognized
	}
	return nil
}

func uniqueJSON(decoder *json.Decoder, depth int) bool {
	if depth > 32 {
		return false
	}
	token, err := decoder.Token()
	if err != nil {
		return false
	}
	delimiter, compound := token.(json.Delim)
	if !compound {
		return true
	}
	if delimiter != '{' && delimiter != '[' {
		return false
	}
	keys := map[string]bool{}
	for decoder.More() {
		if delimiter == '{' {
			token, err := decoder.Token()
			key, valid := token.(string)
			if err != nil || !valid || keys[key] {
				return false
			}
			keys[key] = true
			for _, authority := range []string{"name", "version", "bin", "optionalDependencies", "os", "cpu", "codex"} {
				if strings.EqualFold(key, authority) && key != authority {
					return false
				}
			}
		}
		if !uniqueJSON(decoder, depth+1) {
			return false
		}
	}
	end, err := decoder.Token()
	closing := json.Delim(']')
	if delimiter == '{' {
		closing = '}'
	}
	return err == nil && end == closing
}
