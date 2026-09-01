package phonecore

import "testing"

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
	if err := core.PreparePlatformInstallationSigner(); err != nil {
		t.Fatal(err)
	}
	if len(core.push.data.InstallationKey) != 0 {
		t.Fatal("exportable legacy installation scalar survived platform-signer migration")
	}
}

func TestPreparePlatformInstallationSigner_RefusesExistingAuthority(t *testing.T) {
	core, err := Resume(Config{})
	if err != nil {
		t.Fatal(err)
	}
	core.mu.Lock()
	core.push.data.InstallationID = "already-enrolled"
	core.push.data.InstallationKey = []byte("must-not-be-erased")
	core.mu.Unlock()
	if err := core.PreparePlatformInstallationSigner(); err == nil {
		t.Fatal("migration replaced the signer of an existing installation")
	}
	if string(core.push.data.InstallationKey) != "must-not-be-erased" {
		t.Fatal("failed migration erased the existing installation authority")
	}
}
