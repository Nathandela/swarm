package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
)

const maxTokenKeyringBytes = 64 << 10

var keyVersionPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,32}$`)

type tokenKeyringFile struct {
	Active                string            `json:"active"`
	Keys                  map[string]string `json:"keys"`
	RegistrationDigestKey string            `json:"registration_digest_key"`
}

// loadTokenKeyring reads stable versioned material from a Secret Manager-mounted file.
// It never generates a replacement: a missing, malformed, or incomplete mount fails boot.
func loadTokenKeyring(path string) (map[string][]byte, string, []byte, error) {
	if path == "" {
		return nil, "", nil, errors.New("swarm-pushgw: token keyring file is required")
	}
	fileHandle, err := os.Open(path)
	if err != nil {
		return nil, "", nil, fmt.Errorf("swarm-pushgw: read token keyring: %w", err)
	}
	defer func() { _ = fileHandle.Close() }()
	raw, err := io.ReadAll(io.LimitReader(fileHandle, maxTokenKeyringBytes+1))
	if err != nil {
		return nil, "", nil, errors.New("swarm-pushgw: read token keyring")
	}
	if len(raw) > maxTokenKeyringBytes {
		return nil, "", nil, errors.New("swarm-pushgw: token keyring is too large")
	}
	if err := rejectDuplicateJSONNames(json.NewDecoder(bytes.NewReader(raw))); err != nil {
		return nil, "", nil, errors.New("swarm-pushgw: token keyring contains invalid JSON")
	}
	var file tokenKeyringFile
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return nil, "", nil, errors.New("swarm-pushgw: token keyring contains invalid fields")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, "", nil, errors.New("swarm-pushgw: token keyring contains trailing data")
	}
	if len(file.Keys) == 0 || len(file.Keys) > 16 {
		return nil, "", nil, errors.New("swarm-pushgw: token keyring must contain 1 to 16 keys")
	}
	keys := make(map[string][]byte, len(file.Keys))
	for version, encoded := range file.Keys {
		key, err := base64.RawStdEncoding.DecodeString(encoded)
		if !keyVersionPattern.MatchString(version) || err != nil || len(key) != 32 || base64.RawStdEncoding.EncodeToString(key) != encoded {
			return nil, "", nil, errors.New("swarm-pushgw: every token key must have a bounded version and canonical raw-base64 32-byte value")
		}
		keys[version] = key
	}
	digestKey, err := base64.RawStdEncoding.DecodeString(file.RegistrationDigestKey)
	if err != nil || len(digestKey) != 32 || base64.RawStdEncoding.EncodeToString(digestKey) != file.RegistrationDigestKey {
		return nil, "", nil, errors.New("swarm-pushgw: registration digest key must be raw-base64 encoded 32 bytes")
	}
	if file.Active == "" || keys[file.Active] == nil {
		return nil, "", nil, errors.New("swarm-pushgw: active token key version is unavailable")
	}
	for _, key := range keys {
		if bytes.Equal(key, digestKey) {
			return nil, "", nil, errors.New("swarm-pushgw: registration digest key must be distinct from token encryption keys")
		}
	}
	return keys, file.Active, digestKey, nil
}

func rejectDuplicateJSONNames(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			nameToken, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := nameToken.(string)
			if !ok {
				return errors.New("invalid object name")
			}
			if _, exists := seen[name]; exists {
				return errors.New("duplicate object name")
			}
			seen[name] = struct{}{}
			if err := rejectDuplicateJSONNames(decoder); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := rejectDuplicateJSONNames(decoder); err != nil {
				return err
			}
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	_, err = decoder.Token()
	return err
}
