package main

import (
	"bytes"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
)

const (
	maxAdmissionFileBytes = 64 << 10
	maxAdmissionKeys      = 64
)

type registrationAdmissionFile struct {
	InstallationPublicKeys []string `json:"installation_public_keys"`
}

func loadRegistrationAdmission(path string) (func(string) bool, error) {
	if path == "" {
		return nil, errors.New("swarm-pushgw: registration admission file is required")
	}
	fileHandle, err := os.Open(path)
	if err != nil {
		return nil, errors.New("swarm-pushgw: read registration admission file")
	}
	defer func() { _ = fileHandle.Close() }()
	raw, err := io.ReadAll(io.LimitReader(fileHandle, maxAdmissionFileBytes+1))
	if err != nil {
		return nil, errors.New("swarm-pushgw: read registration admission file")
	}
	if len(raw) > maxAdmissionFileBytes {
		return nil, errors.New("swarm-pushgw: registration admission file is too large")
	}
	if err := rejectDuplicateJSONNames(json.NewDecoder(bytes.NewReader(raw))); err != nil {
		return nil, errors.New("swarm-pushgw: registration admission file contains invalid JSON")
	}
	var file registrationAdmissionFile
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return nil, errors.New("swarm-pushgw: registration admission file contains invalid fields")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("swarm-pushgw: registration admission file contains trailing data")
	}
	if len(file.InstallationPublicKeys) == 0 || len(file.InstallationPublicKeys) > maxAdmissionKeys {
		return nil, errors.New("swarm-pushgw: registration admission file must contain 1 to 64 keys")
	}
	keys := make(map[string]struct{}, len(file.InstallationPublicKeys))
	for _, encoded := range file.InstallationPublicKeys {
		rawKey, err := base64.RawURLEncoding.DecodeString(encoded)
		if err != nil || base64.RawURLEncoding.EncodeToString(rawKey) != encoded {
			return nil, errors.New("swarm-pushgw: registration admission key is not canonical base64url")
		}
		if len(rawKey) != 65 || rawKey[0] != 4 {
			return nil, errors.New("swarm-pushgw: registration admission key is not P-256")
		}
		if _, err := ecdh.P256().NewPublicKey(rawKey); err != nil {
			return nil, errors.New("swarm-pushgw: registration admission key is not P-256")
		}
		if _, exists := keys[encoded]; exists {
			return nil, errors.New("swarm-pushgw: registration admission file contains duplicate keys")
		}
		keys[encoded] = struct{}{}
	}
	return func(publicKey string) bool {
		_, ok := keys[publicKey]
		return ok
	}, nil
}
