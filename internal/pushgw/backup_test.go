package pushgw

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestBackupRestore_RecoversDatabaseAndExactAEADKey(t *testing.T) {
	dir := t.TempDir()
	sourceDB := filepath.Join(dir, "source.db")
	source, err := openStore(sourceDB, sourceDB+".key")
	if err != nil {
		t.Fatalf("open source: %v", err)
	}
	encrypted, err := source.encrypt("fcm-token-restored-exactly")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if err := source.putInstallation("installation", installationRecord{FCMTokenEnc: encrypted}); err != nil {
		t.Fatalf("putInstallation: %v", err)
	}
	sourceKey := source.key
	if err := source.close(); err != nil {
		t.Fatalf("close source: %v", err)
	}

	archive := filepath.Join(dir, "pushgw-backup.tar")
	if err := Backup(sourceDB, archive); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	targetDB := filepath.Join(dir, "restore", "pushgw.db")
	if err := os.Mkdir(filepath.Dir(targetDB), 0o700); err != nil {
		t.Fatalf("mkdir restore: %v", err)
	}
	if err := Restore(targetDB, archive); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	restored, err := openStore(targetDB, targetDB+".key")
	if err != nil {
		t.Fatalf("open restored: %v", err)
	}
	defer func() { _ = restored.close() }()
	if restored.key != sourceKey {
		t.Fatal("restore generated/substituted the AEAD key instead of restoring it")
	}
	rec, found, err := restored.getInstallation("installation")
	if err != nil || !found {
		t.Fatalf("get restored installation: found=%v err=%v", found, err)
	}
	token, err := restored.decrypt(rec.FCMTokenEnc)
	if err != nil || token != "fcm-token-restored-exactly" {
		t.Fatalf("decrypt restored token = %q err=%v", token, err)
	}
}

func TestBackup_RefusesWhileGatewayOwnsTheDatabaseLock(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "pushgw.db")
	store, err := openStore(dbPath, dbPath+".key")
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	defer func() { _ = store.close() }()
	err = Backup(dbPath, filepath.Join(t.TempDir(), "backup.tar"))
	if !errors.Is(err, ErrGatewayRunning) {
		t.Fatalf("Backup with live store = %v, want ErrGatewayRunning", err)
	}
}

func TestRestore_CorruptArchiveLeavesNoPartialDatabaseOrKey(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "corrupt.tar")
	if err := os.WriteFile(archive, []byte("not a push gateway backup"), 0o600); err != nil {
		t.Fatalf("write corrupt archive: %v", err)
	}
	target := filepath.Join(dir, "pushgw.db")
	if err := Restore(target, archive); err == nil {
		t.Fatal("Restore accepted corrupt archive")
	}
	for _, path := range []string{target, target + ".key"} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("failed restore left partial target %s (stat err=%v)", path, err)
		}
	}
}
