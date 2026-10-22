package logins

import (
	"encoding/json"
	"errors"
	"os"
	"runtime"
)

// EraseNativeCredential is for a stopped, isolated enrollment worker's home.
// The caller owns process containment; unrelated native state remains intact.
func EraseNativeCredential(provider, home string) error {
	var keychain credentialKeychain
	if runtime.GOOS == "darwin" {
		keychain = nativeKeychain{}
	}
	return eraseNativeCredential(provider, home, keychain)
}
func eraseNativeCredential(provider, home string, keychain credentialKeychain) error {
	if !validProvider(provider) || checkAbsolute(home, true) != nil {
		return ErrUnsafePath
	}
	before, e := os.Lstat(home)
	if e != nil || !privateInfo(before, true) {
		return ErrUnsafePath
	}
	root, e := os.OpenRoot(home)
	if e != nil {
		return e
	}
	defer func() { _ = root.Close() }()
	opened, e := root.Lstat(".")
	if e != nil || !os.SameFile(before, opened) {
		return ErrUnsafePath
	}
	if provider == ProviderCodex {
		return eraseNativeFields(root, "auth.json", "auth_mode", "tokens", "last_refresh", "OPENAI_API_KEY", "api_key", "access_token", "refresh_token", "id_token")
	}
	if keychain != nil {
		raw, e := keychain.Read(home)
		if e != nil && !errors.Is(e, os.ErrNotExist) {
			return e
		}
		if e == nil {
			next, changed, e := withoutFields(raw, "claudeAiOauth")
			if e != nil {
				return e
			}
			if changed {
				if next == nil {
					e = keychain.Delete(home)
				} else {
					e = keychain.Write(home, next)
				}
				if e != nil {
					return e
				}
			}
		}
	}
	if e = eraseNativeFields(root, ".credentials.json", "claudeAiOauth"); e != nil {
		return e
	}
	return eraseNativeFields(root, ".claude.json", "oauthAccount", "userID")
}

func withoutFields(raw []byte, keys ...string) ([]byte, bool, error) {
	var doc map[string]json.RawMessage
	if len(raw) > maxCredentialBytes || validateJSON(raw) != nil || json.Unmarshal(raw, &doc) != nil || doc == nil {
		return nil, false, ErrInvalidCredentials
	}
	changed := false
	for _, key := range keys {
		if _, ok := doc[key]; ok {
			delete(doc, key)
			changed = true
		}
	}
	if !changed {
		return raw, false, nil
	}
	if len(doc) == 0 {
		return nil, true, nil
	}
	next, e := json.Marshal(doc)
	return next, true, e
}
func eraseNativeFields(root *os.Root, name string, keys ...string) error {
	raw, e := readPrivate(root, name, maxCredentialBytes)
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil {
		return e
	}
	next, changed, e := withoutFields(raw, keys...)
	if e != nil || !changed {
		return e
	}
	if next != nil {
		_, e = durableReplace(root, name, next, writeOps{})
		return e
	}
	if e = root.Remove(name); e != nil {
		return e
	}
	if e = syncRootDir(root, "."); e != nil {
		return ErrDurabilityUncertain
	}
	return nil
}
