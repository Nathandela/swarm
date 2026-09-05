package main

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeAdmissionFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "admission.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func testInstallationPublicKey(t *testing.T) string {
	t.Helper()
	privateKey, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(privateKey.PublicKey().Bytes())
}

func TestLoadRegistrationAdmissionRequiresNonemptyCanonicalP256Keys(t *testing.T) {
	key := testInstallationPublicKey(t)
	path := writeAdmissionFile(t, `{"installation_public_keys":["`+key+`"]}`)
	allow, err := loadRegistrationAdmission(path)
	if err != nil {
		t.Fatalf("loadRegistrationAdmission: %v", err)
	}
	if !allow(key) || allow(testInstallationPublicKey(t)) {
		t.Fatal("admission set did not allow exactly its configured key")
	}

	for name, body := range map[string]string{
		"empty":           `{"installation_public_keys":[]}`,
		"duplicate key":   `{"installation_public_keys":["` + key + `","` + key + `"]}`,
		"non canonical":   `{"installation_public_keys":["` + key + `="]}`,
		"not p256":        `{"installation_public_keys":["` + base64.RawURLEncoding.EncodeToString(make([]byte, 65)) + `"]}`,
		"unknown field":   `{"installation_public_keys":["` + key + `"],"secret":"do-not-echo"}`,
		"duplicate field": `{"installation_public_keys":["` + key + `"],"installation_public_keys":["` + key + `"]}`,
		"trailing":        `{"installation_public_keys":["` + key + `"]}{}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadRegistrationAdmission(writeAdmissionFile(t, body))
			if err == nil {
				t.Fatal("accepted malformed admission file")
			}
			if strings.Contains(err.Error(), "do-not-echo") {
				t.Fatal("error echoed admission file contents")
			}
		})
	}
}

func TestLoadRegistrationAdmissionRequiresAFile(t *testing.T) {
	if _, err := loadRegistrationAdmission(""); err == nil {
		t.Fatal("accepted missing admission file")
	}
}
