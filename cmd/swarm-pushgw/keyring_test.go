package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadTokenKeyringFailsClosedAndKeepsVersions(t *testing.T) {
	if _, _, _, err := loadTokenKeyring(""); err == nil {
		t.Fatal("missing keyring did not fail closed")
	}
	path := filepath.Join(t.TempDir(), "keyring.json")
	key1 := base64.RawStdEncoding.EncodeToString(make([]byte, 32))
	key2 := base64.RawStdEncoding.EncodeToString([]byte(strings.Repeat("x", 32)))
	digest := base64.RawStdEncoding.EncodeToString([]byte(strings.Repeat("d", 32)))
	doc := `{"active":"v2","keys":{"v1":"` + key1 + `","v2":"` + key2 + `"},"registration_digest_key":"` + digest + `"}`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	keys, active, gotDigest, err := loadTokenKeyring(path)
	if err != nil {
		t.Fatal(err)
	}
	if active != "v2" || len(keys) != 2 || string(keys["v2"]) != strings.Repeat("x", 32) || string(gotDigest) != strings.Repeat("d", 32) {
		t.Fatalf("unexpected keyring active=%q versions=%d", active, len(keys))
	}
}

func TestLoadTokenKeyringRejectsNonCanonicalOrAmbiguousInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyring.json")
	key := base64.RawStdEncoding.EncodeToString(make([]byte, 32))
	digest := base64.RawStdEncoding.EncodeToString([]byte(strings.Repeat("d", 32)))
	for name, document := range map[string]string{
		"unknown field":     `{"active":"v1","keys":{"v1":"` + key + `"},"registration_digest_key":"` + digest + `","extra":true}`,
		"duplicate field":   `{"active":"v1","active":"v1","keys":{"v1":"` + key + `"},"registration_digest_key":"` + digest + `"}`,
		"padded base64":     `{"active":"v1","keys":{"v1":"` + key + `="},"registration_digest_key":"` + digest + `"}`,
		"unbounded version": `{"active":"v1","keys":{"` + strings.Repeat("v", 33) + `":"` + key + `"},"registration_digest_key":"` + digest + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, _, err := loadTokenKeyring(path); err == nil {
				t.Fatal("invalid keyring was accepted")
			}
		})
	}
	if err := os.WriteFile(path, make([]byte, maxTokenKeyringBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := loadTokenKeyring(path); err == nil {
		t.Fatal("oversized keyring was accepted")
	}
}

func TestLoadTokenKeyringErrorDoesNotEchoUnknownField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyring.json")
	const secretField = "keyring-secret-must-not-appear"
	document := `{"active":"v1","keys":{"v1":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},"registration_digest_key":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","` + secretField + `":true}`
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, _, err := loadTokenKeyring(path)
	if err == nil {
		t.Fatal("unknown field was accepted")
	}
	if strings.Contains(err.Error(), secretField) {
		t.Fatalf("keyring parse error echoed unknown field material: %q", err)
	}
}
