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
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// TestEnsureOperatorSecret_UnsetConfigIsACleanNoOp asserts the R2 operator
// secret (playbook 6.5) is opt-in: an operator who never sets
// operator_secret_file gets a normal boot with no secret file generated,
// mirroring pushOptions' "unset is supported" direction above.
func TestEnsureOperatorSecret_UnsetConfigIsACleanNoOp(t *testing.T) {
	secret, err := ensureOperatorSecret(relay.DefaultConfig())
	if err != nil {
		t.Fatalf("ensureOperatorSecret with operator_secret_file unset: %v, want a clean no-op", err)
	}
	if secret != "" {
		t.Fatalf("ensureOperatorSecret with operator_secret_file unset returned a secret %q, want empty", secret)
	}
}

// TestEnsureOperatorSecret_GeneratesFileWhenConfigured is the wiring test: the
// binary's own run() path must actually call relay.EnsureOperatorSecret when
// the config names a path, not just leave the library function untested and
// uncalled (the same gap pushOptions closes for the FCM sender above).
func TestEnsureOperatorSecret_GeneratesFileWhenConfigured(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operator.secret")
	cfg := relay.DefaultConfig()
	cfg.OperatorSecretFile = path

	secret, err := ensureOperatorSecret(cfg)
	if err != nil {
		t.Fatalf("ensureOperatorSecret: %v", err)
	}
	if len(secret) < 32 {
		t.Fatalf("ensureOperatorSecret returned a %d-char secret, want a high-entropy value (>= 32)", len(secret))
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("operator secret file was not created by boot wiring: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("operator secret file mode = %o, want 0600", mode)
	}
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

// TestRun_BackupSubcommandRoundTrips is the binary-level wiring test for
// `swarm-relay backup` (playbook 6.5): argv -> config -> relay.Backup, proven
// end to end by then opening the produced file as a config pointing at it.
func TestRun_BackupSubcommandRoundTrips(t *testing.T) {
	dir := t.TempDir()
	cfg := relay.DefaultConfig()
	cfg.DBPath = filepath.Join(dir, "relay.db")
	cfg.Listen = "127.0.0.1:0"
	cfgPath := filepath.Join(dir, "relay.json")
	if err := relay.WriteConfigFile(cfgPath, cfg); err != nil {
		t.Fatalf("WriteConfigFile: %v", err)
	}
	// Boot and close once so a real store file exists at DBPath, mirroring an
	// operator backing up a relay that has actually run (not an empty file).
	srv, err := relay.New(cfg)
	if err != nil {
		t.Fatalf("relay.New: %v", err)
	}
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	dest := filepath.Join(dir, "backup.db")
	if err := run(context.Background(), []string{"backup", "--config", cfgPath, dest}); err != nil {
		t.Fatalf("run backup: %v", err)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("backup subcommand did not produce %s: %v", dest, err)
	}
}

// TestRun_BackupSubcommandRequiresConfigFlag asserts the subcommand shares the
// serve path's fail-closed convention rather than silently guessing a db_path.
func TestRun_BackupSubcommandRequiresConfigFlag(t *testing.T) {
	err := run(context.Background(), []string{"backup", filepath.Join(t.TempDir(), "out.db")})
	if err == nil {
		t.Fatal("run backup without --config returned nil, want a usage error")
	}
}

// TestRun_BackupSubcommandRequiresDestination asserts a missing positional
// destination argument is a clean usage error, not a panic or a silent no-op.
func TestRun_BackupSubcommandRequiresDestination(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "relay.json")
	if err := relay.WriteConfigFile(cfgPath, relay.DefaultConfig()); err != nil {
		t.Fatalf("WriteConfigFile: %v", err)
	}
	err := run(context.Background(), []string{"backup", "--config", cfgPath})
	if err == nil {
		t.Fatal("run backup without a destination path returned nil, want a usage error")
	}
}

// TestRun_RestoreSubcommandRoundTrips mirrors the backup test for `swarm-relay
// restore`: argv -> config -> relay.Restore against a real backup file.
func TestRun_RestoreSubcommandRoundTrips(t *testing.T) {
	dir := t.TempDir()
	cfg := relay.DefaultConfig()
	cfg.DBPath = filepath.Join(dir, "relay.db")
	cfg.Listen = "127.0.0.1:0"
	cfgPath := filepath.Join(dir, "relay.json")
	if err := relay.WriteConfigFile(cfgPath, cfg); err != nil {
		t.Fatalf("WriteConfigFile: %v", err)
	}
	srv, err := relay.New(cfg)
	if err != nil {
		t.Fatalf("relay.New: %v", err)
	}
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	backupPath := filepath.Join(dir, "backup.db")
	if err := relay.Backup(cfg.DBPath, backupPath); err != nil {
		t.Fatalf("relay.Backup: %v", err)
	}
	if err := os.Remove(cfg.DBPath); err != nil {
		t.Fatalf("wipe: %v", err)
	}

	if err := run(context.Background(), []string{"restore", "--config", cfgPath, backupPath}); err != nil {
		t.Fatalf("run restore: %v", err)
	}
	if _, err := os.Stat(cfg.DBPath); err != nil {
		t.Fatalf("restore subcommand did not recreate %s: %v", cfg.DBPath, err)
	}
}

// TestRun_RestoreSubcommandRequiresConfigFlag mirrors the backup usage-error
// case for restore.
func TestRun_RestoreSubcommandRequiresConfigFlag(t *testing.T) {
	err := run(context.Background(), []string{"restore", filepath.Join(t.TempDir(), "backup.db")})
	if err == nil {
		t.Fatal("run restore without --config returned nil, want a usage error")
	}
}

// TestRun_RejectsSubcommandPlacedAfterFlags is the exact ordering
// deploy/relay/swarm-relay.service's ExecStart uses: flags before the
// subcommand. run()'s dispatch only inspects args[0], so "--config" there
// previously fell straight through to runServe, which ignored the leftover
// "backup"/<dest> positional arguments entirely and served until ctx expired
// -- exit 0, no backup file, no error. A misordered backup command must be a
// clean usage error, never something that looks like success.
func TestRun_RejectsSubcommandPlacedAfterFlags(t *testing.T) {
	dir := t.TempDir()
	cfg := relay.DefaultConfig()
	cfg.DBPath = filepath.Join(dir, "relay.db")
	cfg.Listen = "127.0.0.1:0"
	cfgPath := filepath.Join(dir, "relay.json")
	if err := relay.WriteConfigFile(cfgPath, cfg); err != nil {
		t.Fatalf("WriteConfigFile: %v", err)
	}
	dest := filepath.Join(dir, "backup.db")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := run(ctx, []string{"--config", cfgPath, "backup", dest})
	if err == nil {
		t.Fatal("run with a subcommand placed after --config returned nil, want a usage error")
	}
	if _, statErr := os.Stat(dest); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("run with a misordered backup subcommand created a destination file")
	}
}

// TestRun_HealthcheckSubcommandSucceedsWhenReady is the Docker HEALTHCHECK
// entry point (deploy/relay/Dockerfile, playbook 6.5): `swarm-relay
// healthcheck --config <path>` GETs admin_listen's /readyz and exits clean
// when the relay reports ready. distroless has no shell/curl/wget, so this
// subcommand -- not an external tool -- is what Docker execs.
func TestRun_HealthcheckSubcommandSucceedsWhenReady(t *testing.T) {
	dir := t.TempDir()
	cfg := relay.DefaultConfig()
	cfg.DBPath = filepath.Join(dir, "relay.db")
	cfg.Listen = "127.0.0.1:0"
	cfg.AdminListen = "127.0.0.1:0"

	srv, err := relay.New(cfg)
	if err != nil {
		t.Fatalf("relay.New: %v", err)
	}
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	// The config on disk must name the address Start actually bound (":0" picked
	// an ephemeral port), exactly as an operator's real admin_listen would.
	cfg.AdminListen = strings.TrimPrefix(srv.AdminURL(), "http://")
	cfgPath := filepath.Join(dir, "relay.json")
	if err := relay.WriteConfigFile(cfgPath, cfg); err != nil {
		t.Fatalf("WriteConfigFile: %v", err)
	}

	if err := run(context.Background(), []string{"healthcheck", "--config", cfgPath}); err != nil {
		t.Fatalf("run healthcheck against a ready relay: %v, want nil", err)
	}
}

// TestRun_HealthcheckSubcommandFailsWhenNotReady asserts the subcommand's exit
// status actually reflects /readyz rather than only "did the HTTP round-trip
// succeed" -- Docker restarts the container on a non-zero HEALTHCHECK exit, so
// a healthcheck that always returns nil on any 2xx-or-not response would never
// catch a genuinely unready relay. An absurdly high disk_free_min_bytes (1
// TiB) fails /readyz against any real filesystem, deterministically.
func TestRun_HealthcheckSubcommandFailsWhenNotReady(t *testing.T) {
	dir := t.TempDir()
	cfg := relay.DefaultConfig()
	cfg.DBPath = filepath.Join(dir, "relay.db")
	cfg.Listen = "127.0.0.1:0"
	cfg.AdminListen = "127.0.0.1:0"
	cfg.Quotas.DiskFreeMinBytes = 1 << 40 // 1 TiB

	srv, err := relay.New(cfg)
	if err != nil {
		t.Fatalf("relay.New: %v", err)
	}
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	cfg.AdminListen = strings.TrimPrefix(srv.AdminURL(), "http://")
	cfgPath := filepath.Join(dir, "relay.json")
	if err := relay.WriteConfigFile(cfgPath, cfg); err != nil {
		t.Fatalf("WriteConfigFile: %v", err)
	}

	if err := run(context.Background(), []string{"healthcheck", "--config", cfgPath}); err == nil {
		t.Fatal("run healthcheck against a not-ready relay (impossible 1 TiB free-disk floor) returned nil, want an error")
	}
}

// TestRun_HealthcheckSubcommandRequiresConfigFlag mirrors the backup/restore
// usage-error convention.
func TestRun_HealthcheckSubcommandRequiresConfigFlag(t *testing.T) {
	if err := run(context.Background(), []string{"healthcheck"}); err == nil {
		t.Fatal("run healthcheck without --config returned nil, want a usage error")
	}
}

// TestRun_HealthcheckSubcommandRejectsConfigWithNoAdminListen guards against a
// misconfigured container silently reporting healthy: an empty admin_listen
// means there is no /readyz to ask, which must fail the healthcheck rather
// than treat "nothing to check" as "fine".
func TestRun_HealthcheckSubcommandRejectsConfigWithNoAdminListen(t *testing.T) {
	dir := t.TempDir()
	cfg := relay.DefaultConfig()
	cfg.AdminListen = ""
	cfgPath := filepath.Join(dir, "relay.json")
	if err := relay.WriteConfigFile(cfgPath, cfg); err != nil {
		t.Fatalf("WriteConfigFile: %v", err)
	}
	if err := run(context.Background(), []string{"healthcheck", "--config", cfgPath}); err == nil {
		t.Fatal("run healthcheck against a config with no admin_listen returned nil, want an error")
	}
}
