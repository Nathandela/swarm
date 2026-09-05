package main

// ADR-007 B34, machine half (FAILING FIRST): the CLI's owner connection honours the SPKI
// pin in <stateDir>/remote/relay.json.
//
// withMachineRelay is the SECOND machine dial path and it is easy to forget, because it
// was added late (B22) for the two owner acts that must reach the relay at moments when
// the gateway is by construction not running: purging a revoked device's mailbox and
// authorizing a freshly paired one. It dials with the SAME machine relay-auth identity
// the sidecar uses, so a pin applied to the sidecar and not to this is a machine that is
// pinned for its long-lived connection and unpinned for the two connections that carry
// revocation.

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/remote/machineid"
	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/Nathandela/swarm/internal/remote/relaycfg"
)

func b34TLSFrontedRelay(t *testing.T) (wssURL string, cert *x509.Certificate) {
	t.Helper()
	rcfg := relay.DefaultConfig()
	rcfg.Listen = "127.0.0.1:0"
	rcfg.TLSMode = "off"
	rcfg.DBPath = filepath.Join(t.TempDir(), "relay.db")
	srv, err := relay.New(rcfg)
	if err != nil {
		t.Fatalf("relay.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("relay start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	target, err := url.Parse(strings.Replace(srv.URL(), "ws://", "http://", 1))
	if err != nil {
		t.Fatalf("parse relay url: %v", err)
	}
	front := httptest.NewTLSServer(httputil.NewSingleHostReverseProxy(target))
	t.Cleanup(front.Close)
	return strings.Replace(front.URL, "https://", "wss://", 1), front.Certificate()
}

func b34SPKIPin(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return base64.StdEncoding.EncodeToString(sum[:])
}

// b34StateDir provisions what `swarm remote init --relay-url --relay-pin` leaves behind.
func b34StateDir(t *testing.T, cfg relaycfg.Config) string {
	t.Helper()
	stateDir := t.TempDir()
	remoteDir := filepath.Join(stateDir, "remote")
	if err := os.MkdirAll(remoteDir, 0o700); err != nil {
		t.Fatalf("mkdir remote: %v", err)
	}
	id, err := machineid.Generate("b34.local")
	if err != nil {
		t.Fatalf("machineid.Generate: %v", err)
	}
	if err := id.Save(filepath.Join(remoteDir, remoteIdentityFile)); err != nil {
		t.Fatalf("save identity: %v", err)
	}
	if err := relaycfg.Save(stateDir, cfg); err != nil {
		t.Fatalf("save relay.json: %v", err)
	}
	return stateDir
}

// TestPBOPS5_RemoteInitPersistsThePin is the provisioning half: the value the operator
// pastes from the runbook has to survive `swarm remote init` and come back out of the one
// parser that owns the file.
func TestPBOPS5_RemoteInitPersistsThePin(t *testing.T) {
	_, cert := b34TLSFrontedRelay(t)
	pin := b34SPKIPin(cert)

	stateDir := t.TempDir()
	t.Setenv(daemon.EnvStateDir, stateDir)
	var out, errOut strings.Builder
	if code := runRemoteInit([]string{"--relay-url", "wss://relay.example.com", "--relay-pin", pin, "--relay-namespace", "owner"}, &out, &errOut); code != 0 {
		t.Fatalf("remote init --relay-pin exited %d: %s", code, errOut.String())
	}

	cfg, found, err := relaycfg.Load(stateDir)
	if err != nil || !found {
		t.Fatalf("relay.json after remote init: found=%v err=%v", found, err)
	}
	if cfg.SPKIPin != pin {
		t.Fatalf("relay.json carries pin %q, want %q", cfg.SPKIPin, pin)
	}
	sec, err := cfg.Security()
	if err != nil {
		t.Fatalf("the pin `remote init` accepted is one the dial path rejects: %v", err)
	}
	if len(sec.PinnedSPKISHA256) != sha256.Size {
		t.Fatalf("provisioned policy carries a %d-byte pin", len(sec.PinnedSPKISHA256))
	}
}

// TestPBOPS5_RemoteInitRefusesAnUnusablePin covers the three ways the flag is silently
// useless. Each must be refused BEFORE any filesystem work, so the assertion is both the
// exit code and the absence of a provisioned relay.json -- a run that half-provisions is
// the one an operator would never re-check.
func TestPBOPS5_RemoteInitRefusesAnUnusablePin(t *testing.T) {
	wrongSum := sha256.Sum256([]byte("some other relay's public key"))
	good := base64.StdEncoding.EncodeToString(wrongSum[:])

	for name, args := range map[string][]string{
		"pin without a url":  {"--relay-pin", good},
		"pin on a ws:// url": {"--relay-url", "ws://127.0.0.1:9440", "--relay-pin", good},
		"pin is not base64":  {"--relay-url", "wss://relay.example.com", "--relay-pin", "not a pin!!"},
		"pin is truncated": {"--relay-url", "wss://relay.example.com", "--relay-pin",
			base64.StdEncoding.EncodeToString(wrongSum[:16])},
	} {
		t.Run(name, func(t *testing.T) {
			stateDir := t.TempDir()
			t.Setenv(daemon.EnvStateDir, stateDir)
			var out, errOut strings.Builder
			if code := runRemoteInit(append(args, "--relay-namespace", "owner"), &out, &errOut); code == 0 {
				t.Fatalf("remote init accepted %v", args)
			}
			if _, found, _ := relaycfg.Load(stateDir); found {
				t.Fatalf("a refused `remote init` still wrote relay.json; a rejected run must "+
					"provision nothing (stderr was %q)", errOut.String())
			}
		})
	}
}

// TestPBOPS5_TheCLIOwnerConnectionHonoursTheConfiguredPin drives withMachineRelay, the
// production entry point, and proves the matching pin connects before asserting the wrong
// one is refused.
func TestPBOPS5_TheCLIOwnerConnectionHonoursTheConfiguredPin(t *testing.T) {
	wss, cert := b34TLSFrontedRelay(t)

	// ---- control: the matching pin completes the relay-auth handshake ------
	reached := false
	stateDir := b34StateDir(t, relaycfg.Config{RelayURL: wss, OperatorNamespace: "owner", SPKIPin: b34SPKIPin(cert)})
	if err := withMachineRelay(stateDir, func(_ context.Context, cl *relay.Client) error {
		reached = cl.RoutingID() != ""
		return nil
	}); err != nil {
		t.Fatalf("the MATCHING pin was refused, so this rig cannot demonstrate anything: %v", err)
	}
	if !reached {
		t.Fatal("withMachineRelay reported success without giving the caller an authenticated client")
	}

	// ---- the fence: a valid pin for a different key ------------------------
	called := false
	wrongSum := sha256.Sum256([]byte("some other relay's public key"))
	stateDir = b34StateDir(t, relaycfg.Config{
		RelayURL: wss, OperatorNamespace: "owner", SPKIPin: base64.StdEncoding.EncodeToString(wrongSum[:]),
	})
	err := withMachineRelay(stateDir, func(context.Context, *relay.Client) error {
		called = true
		return nil
	})
	if !errors.Is(err, relay.ErrPinMismatch) {
		t.Fatalf("withMachineRelay against a relay that does not match the configured pin "+
			"returned %v, want relay.ErrPinMismatch", err)
	}
	if called {
		t.Fatal("withMachineRelay ran the owner's operation over a connection whose peer failed the pin")
	}
}
