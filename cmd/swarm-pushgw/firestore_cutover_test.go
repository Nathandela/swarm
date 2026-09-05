package main

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestFirestoreCutoverRejectsLegacyRoutesAndDBFlag(t *testing.T) {
	for name, test := range map[string]struct {
		args []string
		want string
	}{
		"backup":             {args: []string{"backup"}, want: `unknown command "backup"`},
		"restore":            {args: []string{"restore"}, want: `unknown command "restore"`},
		"db flag":            {args: []string{"-db", "pushgw.db"}, want: "flag provided but not defined: -db"},
		"retention interval": {args: []string{"-retention-interval", "1h"}, want: "flag provided but not defined: -retention-interval"},
	} {
		t.Run(name, func(t *testing.T) {
			err := run(context.Background(), test.args)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("run(%q) error = %v, want %q", test.args, err, test.want)
			}
		})
	}
}

func TestFirestoreModeRejectsImplicitOrProductionEmulator(t *testing.T) {
	t.Setenv("FIRESTORE_EMULATOR_HOST", "127.0.0.1:8799")
	for name, args := range map[string][]string{
		"production":   {"-insecure-http"},
		"implicit dev": {"-dev", "-insecure-http"},
	} {
		t.Run(name, func(t *testing.T) {
			err := run(context.Background(), args)
			if err == nil || !strings.Contains(err.Error(), "Firestore emulator") {
				t.Fatalf("run error = %v, want fail-fast Firestore emulator error", err)
			}
		})
	}
}

func TestFirestoreDevRequiresExplicitEmulatorHost(t *testing.T) {
	t.Setenv("FIRESTORE_EMULATOR_HOST", "")
	err := run(context.Background(), []string{"-dev", "-allow-firestore-emulator", "-insecure-http"})
	if err == nil || !strings.Contains(err.Error(), "FIRESTORE_EMULATOR_HOST") {
		t.Fatalf("run error = %v, want missing emulator host", err)
	}
}

func TestFirestoreProductionAuthorityIsExplicitAndPinned(t *testing.T) {
	for name, values := range map[string][2]string{
		"missing project":     {"", "push-v2-owner-pilot"},
		"wrong project":       {"other", "push-v2-owner-pilot"},
		"missing namespace":   {productionGCPProjectID, ""},
		"malformed namespace": {productionGCPProjectID, "stale/path"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateFirestoreMode(false, false, values[0], values[1], ""); err == nil {
				t.Fatal("run accepted missing or wrong Firestore authority")
			}
		})
	}
	if err := validateFirestoreMode(false, false, productionGCPProjectID, "staging-restore-1", ""); err != nil {
		t.Fatalf("valid explicit isolated namespace rejected: %v", err)
	}
}

func TestResolveListenAddressUsesCloudRunPortUnlessExplicitlyOverridden(t *testing.T) {
	for name, test := range map[string]struct {
		flagValue string
		port      string
		explicit  bool
		want      string
		wantErr   bool
	}{
		"local default":     {flagValue: ":8443", want: ":8443"},
		"cloud run":         {flagValue: ":8443", port: "8080", want: ":8080"},
		"explicit override": {flagValue: "127.0.0.1:9000", port: "bad", explicit: true, want: "127.0.0.1:9000"},
		"non canonical":     {flagValue: ":8443", port: "08080", wantErr: true},
		"out of range":      {flagValue: ":8443", port: "65536", wantErr: true},
		"non numeric":       {flagValue: ":8443", port: "bad", wantErr: true},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := resolveListenAddress(test.flagValue, test.port, test.explicit)
			if (err != nil) != test.wantErr || got != test.want {
				t.Fatalf("resolveListenAddress = %q, %v; want %q, error=%v", got, err, test.want, test.wantErr)
			}
		})
	}
}

func TestProductionRejectsTrustedForwardingHeadersBeforeHostedEvidence(t *testing.T) {
	if _, err := validatedTrustedProxies(false, "10.0.0.0/8"); err == nil {
		t.Fatal("production accepted trusted proxy configuration before hosted evidence")
	}
	if got, err := validatedTrustedProxies(true, "127.0.0.1/32"); err != nil || len(got) != 1 {
		t.Fatalf("explicit dev proxy validation = %v, %v", got, err)
	}
}

func TestRetentionSubcommandRunsOneBoundedFirestoreSweep(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("requires Firestore emulator")
	}
	if err := run(context.Background(), append([]string{"retention"}, devRuntimeArgs(t)...)); err != nil {
		t.Fatalf("retention: %v", err)
	}
}
