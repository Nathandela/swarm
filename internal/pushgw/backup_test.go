package pushgw

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

func TestBackup_NeverOverwritesExistingArchive(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "pushgw.db")
	store, err := openStore(dbPath, dbPath+".key")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.close(); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "backup.tar")
	const existing = "existing-backup-must-survive"
	if err := os.WriteFile(dest, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Backup(dbPath, dest); err == nil {
		t.Fatal("Backup overwrote an existing destination")
	}
	got, err := os.ReadFile(dest)
	if err != nil || string(got) != existing {
		t.Fatalf("existing backup changed: got=%q err=%v", got, err)
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

func TestRestore_ResumesCrashBetweenKeyAndDatabasePublication(t *testing.T) {
	dir := t.TempDir()
	stageDir := filepath.Join(dir, ".swarm-pushgw-restore-test")
	if err := os.Mkdir(stageDir, 0o700); err != nil {
		t.Fatal(err)
	}
	stageDB := filepath.Join(stageDir, backupDBName)
	st, err := openStore(stageDB, filepath.Join(stageDir, backupKeyName))
	if err != nil {
		t.Fatalf("open staged store: %v", err)
	}
	wantKey := st.key
	if err := st.close(); err != nil {
		t.Fatalf("close staged store: %v", err)
	}
	manifest, err := manifestForRestoreFiles(stageDB, filepath.Join(stageDir, backupKeyName))
	if err != nil {
		t.Fatalf("manifestForRestoreFiles: %v", err)
	}
	target := filepath.Join(dir, "pushgw.db")
	publication, err := prepareRestorePublication(target, stageDir, manifest)
	if err != nil {
		t.Fatalf("prepareRestorePublication: %v", err)
	}
	// Simulate power loss after the key hard-link is durable but before the DB
	// link exists. The marker must make openStore refuse this half-pair.
	if err := os.Link(filepath.Join(stageDir, backupKeyName), target+".key"); err != nil {
		t.Fatalf("publish key half: %v", err)
	}
	if _, err := openStore(target, target+".key"); err == nil || !strings.Contains(err.Error(), "restore") {
		t.Fatalf("openStore during interrupted restore = %v, want restore-in-progress refusal", err)
	}
	if err := recoverRestorePublication(target); err != nil {
		t.Fatalf("recoverRestorePublication: %v", err)
	}
	if _, err := os.Stat(publication.markerPath); !os.IsNotExist(err) {
		t.Fatalf("recovered publication left marker: %v", err)
	}
	restored, err := openStore(target, target+".key")
	if err != nil {
		t.Fatalf("open recovered store: %v", err)
	}
	defer func() { _ = restored.close() }()
	if restored.key != wantKey {
		t.Fatal("recovered publication did not preserve exact AEAD key")
	}
}

func TestRestore_PublicationNeverOverwritesRacingTarget(t *testing.T) {
	dir := t.TempDir()
	stageDir := filepath.Join(dir, ".swarm-pushgw-restore-test")
	if err := os.Mkdir(stageDir, 0o700); err != nil {
		t.Fatal(err)
	}
	stageDB := filepath.Join(stageDir, backupDBName)
	st, err := openStore(stageDB, filepath.Join(stageDir, backupKeyName))
	if err != nil {
		t.Fatalf("open staged store: %v", err)
	}
	if err := st.close(); err != nil {
		t.Fatal(err)
	}
	manifest, err := manifestForRestoreFiles(stageDB, filepath.Join(stageDir, backupKeyName))
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "pushgw.db")
	publication, err := prepareRestorePublication(target, stageDir, manifest)
	if err != nil {
		t.Fatal(err)
	}
	const competing = "do-not-overwrite"
	if err := os.WriteFile(target, []byte(competing), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := publishRestorePublication(publication); err == nil {
		t.Fatal("publication overwrote a target created after preflight")
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != competing {
		t.Fatalf("racing target changed: got=%q err=%v", got, err)
	}
	if _, err := os.Stat(target + ".key"); !os.IsNotExist(err) {
		t.Fatalf("failed exclusive publication left its key half: %v", err)
	}
}

func TestBackupAndRestoreRejectUnboundedArtifacts(t *testing.T) {
	if err := validateRestoreEntrySize(backupDBName, maxBackupDBBytes+1); err == nil {
		t.Fatal("restore accepted a database entry over the configured ceiling")
	}
	if err := validateRestoreEntrySize(backupKeyName, 33); err == nil {
		t.Fatal("restore accepted an oversized AEAD key entry")
	}
}

func TestRestore_RejectsMarkerWhoseStageIsOutsideTargetDirectory(t *testing.T) {
	root := t.TempDir()
	targetDir := filepath.Join(root, "target")
	victimDir := filepath.Join(root, "must-survive")
	if err := os.MkdirAll(targetDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(victimDir, 0o700); err != nil {
		t.Fatal(err)
	}
	stageDB := filepath.Join(victimDir, backupDBName)
	st, err := openStore(stageDB, filepath.Join(victimDir, backupKeyName))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.close(); err != nil {
		t.Fatal(err)
	}
	manifest, err := manifestForRestoreFiles(stageDB, filepath.Join(victimDir, backupKeyName))
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(targetDir, "pushgw.db")
	marker := restorePublication{
		Version: backupFormatVersion, TargetDB: target, StageDir: victimDir, Manifest: manifest,
	}
	data, err := json.Marshal(marker)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(restoreMarkerPath(target), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := recoverRestorePublication(target); err == nil {
		t.Fatal("recovery accepted a marker pointing outside the target directory")
	}
	if _, err := os.Stat(victimDir); err != nil {
		t.Fatalf("rejected marker deleted its outside stage directory: %v", err)
	}
}
