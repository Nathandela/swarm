package opencodebridge

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	"github.com/Nathandela/swarm/internal/adapter"
)

var errPrivateBinding = errors.New("opencode bridge: invalid private binding")

func bindingPath(value string) bool {
	return filepath.IsAbs(value) && filepath.Clean(value) == value && len(value) <= 4096 && !strings.ContainsFunc(value, unicode.IsControl)
}

func bindingHex(value string) bool {
	if len(value) != 32 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// ReadBinding opens only an owned, exclusive credential file inside its private socket directory.
func ReadBinding(path string) (Binding, error) {
	if !bindingPath(path) {
		return Binding{}, errPrivateBinding
	}
	parent := filepath.Dir(path)
	physical, err := filepath.EvalSymlinks(parent)
	if err != nil || physical != parent {
		return Binding{}, errPrivateBinding
	}
	info, err := os.Lstat(parent)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return Binding{}, errPrivateBinding
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) {
		return Binding{}, errPrivateBinding
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		return Binding{}, errPrivateBinding
	}
	defer func() { _ = root.Close() }()
	actual, err := root.Stat(".")
	if err != nil || !os.SameFile(info, actual) {
		return Binding{}, errPrivateBinding
	}
	f, err := root.OpenFile(filepath.Base(path), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return Binding{}, errPrivateBinding
	}
	defer func() { _ = f.Close() }()
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > 16<<10 {
		return Binding{}, errPrivateBinding
	}
	stat, ok = info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) || stat.Nlink != 1 {
		return Binding{}, errPrivateBinding
	}
	raw, err := io.ReadAll(io.LimitReader(f, (16<<10)+1))
	if err != nil || len(raw) > 16<<10 || !utf8.Valid(raw) {
		return Binding{}, errPrivateBinding
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	start, err := dec.Token()
	if err != nil || start != json.Delim('{') {
		return Binding{}, errPrivateBinding
	}
	seen := map[string]bool{}
	for dec.More() {
		token, err := dec.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] {
			return Binding{}, errPrivateBinding
		}
		switch key {
		case "version", "workerID", "sessionInstance", "rootID", "directory", "backendGeneration", "socketPath", "token", "mode", "nativeCreatedAtMS", "initialPrompt":
		default:
			return Binding{}, errPrivateBinding
		}
		seen[key] = true
		var value json.RawMessage
		if dec.Decode(&value) != nil {
			return Binding{}, errPrivateBinding
		}
	}
	end, err := dec.Token()
	if err != nil || end != json.Delim('}') || len(seen) != 11 {
		return Binding{}, errPrivateBinding
	}
	if _, err := dec.Token(); err != io.EOF {
		return Binding{}, errPrivateBinding
	}
	var b Binding
	if json.Unmarshal(raw, &b) != nil || b.Version != 1 || !adapter.IsOpenCodeConversationID(b.RootID) || !bindingHex(b.SessionInstance) || !bindingHex(b.BackendGeneration) || b.WorkerID == "" || len(b.WorkerID) > 128 || strings.ContainsFunc(b.WorkerID, unicode.IsControl) {
		return Binding{}, errPrivateBinding
	}
	if !bindingPath(b.Directory) || !bindingPath(b.SocketPath) || len(b.SocketPath) > 100 || filepath.Dir(b.SocketPath) != parent {
		return Binding{}, errPrivateBinding
	}
	if len(b.Token) < 32 || len(b.Token) > 256 || strings.ContainsFunc(b.Token, unicode.IsControl) || b.NativeCreatedAtMS > 1<<53-1 {
		return Binding{}, errPrivateBinding
	}
	if b.Mode != "create" && b.Mode != "resume" || b.Mode == "create" && b.NativeCreatedAtMS != 0 || b.Mode == "resume" && b.NativeCreatedAtMS <= 0 {
		return Binding{}, errPrivateBinding
	}
	return b, nil
}
