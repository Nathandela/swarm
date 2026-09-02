package phonecore

import (
	"crypto/ecdh"
	"encoding/base64"
	"fmt"
	"testing"
)

func platformSignerTestPublic(multiplier byte) []byte {
	scalar := make([]byte, 32)
	scalar[len(scalar)-1] = multiplier
	private, err := ecdh.P256().NewPrivateKey(scalar)
	if err != nil {
		panic(err)
	}
	return private.PublicKey().Bytes()
}

func TestPreparePlatformInstallationSigner_RemovesUnregisteredExportableKey(t *testing.T) {
	core, err := Resume(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := core.InstallationSigner(); err != nil {
		t.Fatal(err)
	}
	if len(core.push.data.InstallationKey) == 0 {
		t.Fatal("fixture did not mint a legacy installation scalar")
	}
	public := platformSignerTestPublic(1)
	if err := core.PreparePlatformInstallationSigner(public); err != nil {
		t.Fatal(err)
	}
	if len(core.push.data.InstallationKey) != 0 {
		t.Fatal("exportable legacy installation scalar survived platform-signer migration")
	}
	if got := core.push.data.InstallationPublicKey; string(got) != string(public) {
		t.Fatalf("platform public key = %x, want %x", got, public)
	}
}

func TestPreparePlatformInstallationSigner_RestartAcceptsOnlyExactExistingAuthority(t *testing.T) {
	core, err := Resume(Config{})
	if err != nil {
		t.Fatal(err)
	}
	public := platformSignerTestPublic(1)
	core.mu.Lock()
	core.push.data.InstallationID = "already-enrolled"
	core.push.data.InstallationPublicKey = append([]byte(nil), public...)
	core.mu.Unlock()
	if err := core.PreparePlatformInstallationSigner(public); err != nil {
		t.Fatalf("restart rejected the byte-identical platform signer: %v", err)
	}
	if err := core.PreparePlatformInstallationSigner(platformSignerTestPublic(2)); err == nil {
		t.Fatal("restart accepted a different signer for the existing installation")
	}
	if got := core.push.data.InstallationPublicKey; string(got) != string(public) {
		t.Fatal("wrong-key refusal replaced the durable platform authority")
	}
}

func TestPreparePlatformInstallationSigner_LegacyPlatformModeMigratesOnceButNeverOverExportableAuthority(t *testing.T) {
	public := platformSignerTestPublic(1)
	core, err := Resume(Config{})
	if err != nil {
		t.Fatal(err)
	}
	core.push.data.InstallationID = "legacy-platform-installation"
	if err := core.PreparePlatformInstallationSigner(public); err != nil {
		t.Fatalf("one-time platform-mode migration: %v", err)
	}
	if got := core.push.data.InstallationPublicKey; string(got) != string(public) {
		t.Fatalf("migrated public key = %x, want %x", got, public)
	}

	withExportable, err := Resume(Config{})
	if err != nil {
		t.Fatal(err)
	}
	withExportable.push.data.InstallationID = "legacy-go-installation"
	withExportable.push.data.InstallationKey = []byte("known-exportable-authority")
	if err := withExportable.PreparePlatformInstallationSigner(public); err == nil {
		t.Fatal("migration overwrote an existing exportable installation authority")
	}
}

func TestPreparePlatformInstallationSigner_LegacyPendingRegisterBindsExactRequestKey(t *testing.T) {
	public := platformSignerTestPublic(1)
	body := []byte(fmt.Sprintf(`{"installation_public_key":%q,"fcm_token":"token","attestation":{"provider":"play_integrity","token":"opaque"}}`,
		base64.RawURLEncoding.EncodeToString(public)))
	newCore := func() *Core {
		core, err := Resume(Config{})
		if err != nil {
			t.Fatal(err)
		}
		core.push.data.PendingRegister = &pendingRegisterRec{IdemKey: "idem", Body: body, FCMToken: "token"}
		return core
	}
	if err := newCore().PreparePlatformInstallationSigner(public); err != nil {
		t.Fatalf("exact pending-register signer: %v", err)
	}
	if err := newCore().PreparePlatformInstallationSigner(platformSignerTestPublic(2)); err == nil {
		t.Fatal("accepted signer different from outcome-unknown registration body")
	}
}

func TestPreparePlatformInstallationSigner_RejectsMalformedPublicKey(t *testing.T) {
	core, err := Resume(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := core.PreparePlatformInstallationSigner(make([]byte, 65)); err == nil {
		t.Fatal("accepted a non-canonical/non-curve platform public key")
	}
}
