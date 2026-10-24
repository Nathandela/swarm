package shim

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"

	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
)

// A token-free birth still requires the exact private reservation. Check before
// installing custody, which removes the previous native-process stop evidence.
func validateCodexExternalReservation(cfg Config) error {
	if cfg.LoginRuntime == nil || !cfg.LoginRuntime.ExternalAuth || cfg.LoginSwitchID != "" {
		return nil
	}
	if !persist.ValidID(cfg.SessionID) || cfg.SessionDir != filepath.Join(cfg.LoginStateRoot, cfg.SessionID) {
		return errLoginResolution
	}
	root, err := os.OpenRoot(cfg.LoginStateRoot)
	if err != nil {
		return errLoginResolution
	}
	defer func() { _ = root.Close() }()
	info, err := root.Lstat(cfg.SessionID)
	stat, ok := infoSys(info)
	if err != nil || !ok || !info.IsDir() || info.Mode().Perm()&0077 != 0 || stat.Uid != uint32(os.Getuid()) {
		return errLoginResolution
	}
	f, err := root.OpenFile(filepath.Join(cfg.SessionID, "meta.json"), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return errLoginResolution
	}
	defer func() { _ = f.Close() }()
	info, err = f.Stat()
	stat, ok = infoSys(info)
	if err != nil || !ok || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || stat.Uid != uint32(os.Getuid()) || stat.Nlink != 1 || info.Size() > 1<<20 {
		return errLoginResolution
	}
	raw, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if err != nil || len(raw) > 1<<20 || !uniqueBirthJSON(raw) {
		return errLoginResolution
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return errLoginResolution
	}
	for key := range fields {
		lower := strings.ToLower(key)
		switch lower {
		case "schema_version", "id", "agent_type", "login_runtime", "input_embargo", "cli_identity", "resumed_from", "conversation_id":
			if key != lower {
				return errLoginResolution
			}
		}
	}
	if !sameBirthJSON(fields["login_runtime"], cfg.LoginRuntime) {
		return errLoginResolution
	}
	if cfg.CLIIdentity != nil && !sameBirthJSON(fields["cli_identity"], cfg.CLIIdentity) {
		return errLoginResolution
	}
	var meta persist.Meta
	if json.Unmarshal(raw, &meta) != nil || meta.SchemaVersion != persist.LoginSchemaVersion || meta.ID != cfg.SessionID || meta.AgentType != logins.ProviderCodex || meta.LoginRuntime == nil || *meta.LoginRuntime != *cfg.LoginRuntime || meta.InputEmbargo != cfg.InputEmbargo || !reflect.DeepEqual(meta.CLIIdentity, cfg.CLIIdentity) {
		return errLoginResolution
	}
	if cfg.InputEmbargo != "" {
		s, err := logins.OpenReadOnly(cfg.LoginStateRoot)
		if err != nil {
			return errLoginResolution
		}
		defer func() { _ = s.Close() }()
		if s.ValidateCodexBootstrapBirth(cfg.InputEmbargo, meta.ResumedFrom, meta.ConversationID, meta.ID) != nil {
			return errLoginResolution
		}
	}
	return nil
}

func uniqueBirthJSON(raw []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(raw))
	var value func(int) bool
	value = func(depth int) bool {
		if depth > 64 {
			return false
		}
		token, err := dec.Token()
		if err != nil {
			return false
		}
		delim, container := token.(json.Delim)
		if !container {
			return true
		}
		if delim != '{' && delim != '[' {
			return false
		}
		seen := map[string]bool{}
		for dec.More() {
			if delim == '{' {
				key, err := dec.Token()
				if err != nil {
					return false
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return false
				}
				seen[name] = true
			}
			if !value(depth + 1) {
				return false
			}
		}
		_, err = dec.Token()
		return err == nil
	}
	if !value(0) {
		return false
	}
	_, err := dec.Token()
	return err == io.EOF
}

func sameBirthJSON(raw json.RawMessage, expected any) bool {
	encoded, err := json.Marshal(expected)
	if err != nil {
		return false
	}
	var a, b any
	left, right := json.NewDecoder(bytes.NewReader(raw)), json.NewDecoder(bytes.NewReader(encoded))
	left.UseNumber()
	right.UseNumber()
	return left.Decode(&a) == nil && right.Decode(&b) == nil && reflect.DeepEqual(a, b)
}
