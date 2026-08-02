package main

// Main-level RED test for the swarm-relay binary (R-REL.9). The binary parses
// argv, reads one config file, and boots the relay. This exercises the binary's
// own wiring (argv -> run) and its clean error handling; the full config-boot
// round-trip is covered in-package by relay.TestRelay_BootsFromConfigLocalhost.

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/relay"
)

// TestMain_RunRejectsMissingConfig asserts a missing config path is a clean
// error, not a panic — the binary fails closed when it cannot read its config.
func TestMain_RunRejectsMissingConfig(t *testing.T) {
	err := run(context.Background(), []string{"--config", filepath.Join(t.TempDir(), "nope.conf")})
	if err == nil {
		t.Fatalf("run with a missing config file returned nil, want an error")
	}
}

// TestMain_RunRequiresConfigFlag asserts the binary refuses to boot without a
// config file rather than silently starting on unspecified defaults.
func TestMain_RunRequiresConfigFlag(t *testing.T) {
	if err := run(context.Background(), nil); err == nil {
		t.Fatalf("run without --config returned nil, want a usage error")
	}
}

// TestPushOptions_UnsetCredentialBootsWithNoSink pins the supported no-push
// configuration: an operator who has not provisioned FCM gets a relay that runs
// with every other path unaffected (PB-PUSH-5, "the system works without push").
func TestPushOptions_UnsetCredentialBootsWithNoSink(t *testing.T) {
	opts, err := pushOptions(relay.DefaultConfig())
	if err != nil {
		t.Fatalf("pushOptions with no credential: %v, want a clean no-sink boot", err)
	}
	if len(opts) != 0 {
		t.Fatalf("pushOptions returned %d options for an unset credential, want none", len(opts))
	}
}

// TestPushOptions_ValidCredentialInstallsTheSink is the assertion the two around
// it cannot make, and it is the one that matters most.
//
// "No error" and "no options when unset" are both satisfied perfectly by a
// pushOptions that NEVER installs a sink — which is exactly the state this slice
// found the tree in, an FCM sender with zero production callers whose every unit
// test was green. Without this, the relay binary could ship with the push
// transport built and dropped and nothing anywhere would fail.
func TestPushOptions_ValidCredentialInstallsTheSink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "creds.json")
	if err := os.WriteFile(path, validServiceAccount(t), 0o600); err != nil {
		t.Fatalf("write credential: %v", err)
	}
	cfg := relay.DefaultConfig()
	cfg.PushCredentials = path

	opts, err := pushOptions(cfg)
	if err != nil {
		t.Fatalf("pushOptions with a well-formed credential: %v", err)
	}
	if len(opts) != 1 {
		t.Fatalf("pushOptions returned %d options for a valid credential, want exactly 1 (the "+
			"WithPushSink installing the FCM sender): a relay that reads the credential and then "+
			"drops the transport is a relay with no push at all, and nothing else here would notice",
			len(opts))
	}
	// The option must be applicable to a real Server: relay.New running it is what
	// proves it is a usable relay.Option and not, say, a nil entry.
	cfg.DBPath = filepath.Join(t.TempDir(), "relay.db")
	srv, err := relay.New(cfg, opts...)
	if err != nil {
		t.Fatalf("relay.New with the push option: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
}

// validServiceAccount builds a syntactically well-formed Google service-account
// document over a freshly generated key. It authorises nothing anywhere: no
// Google project exists, nothing here contacts a provider, and PB-E2E-5 (real
// delivery to a real handset) stays DEFERRED.
func validServiceAccount(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
	}
	doc, err := json.Marshal(map[string]string{
		"type":           "service_account",
		"project_id":     "swarm-relay-test-project",
		"private_key_id": "kid-1",
		"private_key":    string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"client_email":   "pusher@swarm-relay-test-project.iam.gserviceaccount.com",
		"token_uri":      "https://oauth2.example.invalid/token",
	})
	if err != nil {
		t.Fatalf("marshal service account: %v", err)
	}
	return doc
}

// TestPushOptions_BrokenCredentialFailsTheBoot is the direction that matters
// (PB-PUSH-5). A relay that boots happily on a credential it cannot use looks
// healthy while push is dead, and the operator learns about it from a user who
// missed a hand-off. Each case is a way the credential can be wrong in
// production: never provisioned at that path, or provisioned as something that
// is not a service account.
func TestPushOptions_BrokenCredentialFailsTheBoot(t *testing.T) {
	dir := t.TempDir()
	garbage := filepath.Join(dir, "creds.json")
	if err := os.WriteFile(garbage, []byte("{not a service account"), 0o600); err != nil {
		t.Fatalf("write credential: %v", err)
	}
	for name, path := range map[string]string{
		"missing file":     filepath.Join(dir, "absent.json"),
		"unparseable file": garbage,
	} {
		t.Run(name, func(t *testing.T) {
			cfg := relay.DefaultConfig()
			cfg.PushCredentials = path
			if _, err := pushOptions(cfg); err == nil {
				t.Fatal("pushOptions accepted a broken push credential: the relay would boot looking " +
					"healthy with push silently dead")
			}
		})
	}
}
