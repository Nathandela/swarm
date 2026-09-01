// Command swarm-pushgw boots the Swarm push gateway
// (docs/specifications/push-gateway-api.md, ADR-015). It fails closed: a missing -db, an
// unreadable/wrong-project Google runtime credential, or an unusable quota bucket is a
// clean error rather than a boot on unspecified defaults.
//
// PG-TR-1 requires HTTPS on the ordinary Web PKI for every operation, TLS 1.3 as the floor.
// This binary satisfies that in one of two DECLARED ways, never silently: pass -tls-cert
// and -tls-key to terminate TLS in-process (ListenAndServeTLS, MinVersion TLS 1.3), or pass
// -insecure-http to declare that TLS is terminated by a reverse proxy in front of this
// process -- the recorded deployment assumption PG-TR-1's absence of a self-hosted relay's
// certificate-provisioning excuses invites. -insecure-http is a deliberate, explicit flag
// specifically so an operator cannot reach a plaintext listener by omission. A
// TLS-terminating proxy also means every caller presents the proxy's address, not its own
// (PG-Q-4): pair -insecure-http with -trusted-proxies naming that proxy's CIDR(s), the same
// trusted-proxy principle internal/remote/relay's bundle already uses, so quota accounting
// still keys on the real caller instead of collapsing every source into one shared bucket.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Nathandela/swarm/internal/pushgw"
	"github.com/Nathandela/swarm/internal/remote/push"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// defaultRetentionInterval is the production cadence for the retention sweep (spec
// section 8.1) when -retention-interval is not set.
const defaultRetentionInterval = time.Hour

// shutdownTimeout bounds how long a graceful shutdown waits for in-flight requests.
const shutdownTimeout = 10 * time.Second

const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 15 * time.Second
	writeTimeout      = 30 * time.Second
	idleTimeout       = 60 * time.Second
	maxHeaderBytes    = 32 << 10

	productionGCPProjectID                 = "swarm-8404f"
	productionPlaySigningCertificateSHA256 = "hz8YTGhTTgpYccjMiQDrhx5HcddqRsTu1HRcmhhknmU"
	fcmMessagingScope                      = "https://www.googleapis.com/auth/firebase.messaging"
	playIntegrityScope                     = "https://www.googleapis.com/auth/playintegrity"
	productionProviderTimeout              = 15 * time.Second
	productionPlayVerdictMaxAge            = 2 * time.Minute
	productionPlayVerdictMaxFutureSkew     = 30 * time.Second
)

type runtimeGoogleCredentials struct {
	ProjectID   string
	TokenSource oauth2.TokenSource
}

type runtimeCredentialLoader func(context.Context, string, []string) (runtimeGoogleCredentials, error)

type productionDependencyConfig struct {
	CredentialPath        string
	ProjectID             string
	ProjectNumber         int64
	PackageName           string
	SigningCertificateSHA string
}

type productionDependencies struct {
	sender    pushgw.WakeSender
	attestor  pushgw.AttestationVerifier
	readiness pushgw.DeploymentReadiness
}

func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "backup":
			return runBackup(args[1:])
		case "restore":
			return runRestore(args[1:])
		case "healthcheck":
			return runHealthcheck(ctx, args[1:])
		}
	}
	return runServe(ctx, args)
}

func runBackup(args []string) error {
	fs := flag.NewFlagSet("swarm-pushgw backup", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dbPath := fs.String("db", "", "path to the stopped gateway database")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dbPath == "" || fs.NArg() != 1 {
		return errors.New("usage: swarm-pushgw backup -db <pushgw.db> <archive.tar>")
	}
	return pushgw.Backup(*dbPath, fs.Arg(0))
}

func runRestore(args []string) error {
	fs := flag.NewFlagSet("swarm-pushgw restore", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dbPath := fs.String("db", "", "path for the restored gateway database")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dbPath == "" || fs.NArg() != 1 {
		return errors.New("usage: swarm-pushgw restore -db <pushgw.db> <archive.tar>")
	}
	return pushgw.Restore(*dbPath, fs.Arg(0))
}

func runHealthcheck(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("swarm-pushgw healthcheck", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	url := fs.String("url", "", "loopback readiness URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *url == "" || fs.NArg() != 0 {
		return errors.New("usage: swarm-pushgw healthcheck -url http://127.0.0.1:<port>/readyz")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, *url, nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("swarm-pushgw healthcheck: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("swarm-pushgw healthcheck: readiness returned %d, want 200", resp.StatusCode)
	}
	return nil
}

func runServe(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("swarm-pushgw", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	listen := fs.String("listen", ":8443", "address to serve the gateway on")
	adminListen := fs.String("admin-listen", "", "separate loopback-only address for /healthz, /readyz, and /metrics (empty disables)")
	dbPath := fs.String("db", "", "path to the gateway's bbolt file (required)")
	devMode := fs.Bool("dev", false, "explicit fail-closed development mode: no Google credential, FCM delivery, or Play registration")
	googleCredentials := fs.String("google-credentials", "", "path to a swarm-8404f service-account JSON for FCM and Play Integrity (empty uses Application Default Credentials)")
	gcpProjectID := fs.String("gcp-project-id", productionGCPProjectID, "Google Cloud project id (production is fixed)")
	gcpProjectNumber := fs.Int64("gcp-project-number", pushgw.ProductionCloudProjectNumber, "Google Cloud project number (production is fixed)")
	androidPackage := fs.String("android-package", pushgw.ProductionAndroidPackage, "Google Play Android package (production is fixed)")
	playSigningCert := fs.String("play-signing-cert-sha256", productionPlaySigningCertificateSHA256, "allowed Play App Signing certificate SHA-256, canonical base64url")
	retentionInterval := fs.Duration("retention-interval", defaultRetentionInterval, "how often the retention sweep (spec section 8.1) runs")

	tlsCert := fs.String("tls-cert", "", "path to a PEM certificate; terminates TLS in-process (PG-TR-1)")
	tlsKey := fs.String("tls-key", "", "path to the PEM private key matching -tls-cert")
	insecureHTTP := fs.Bool("insecure-http", false, "serve plain HTTP, declaring that TLS is terminated by a reverse proxy in front of this process (PG-TR-1) -- pair with -trusted-proxies")
	trustedProxies := fs.String("trusted-proxies", "", "comma-separated CIDRs of reverse proxies to trust for X-Forwarded-For (PG-Q-4); only consulted when the TCP peer itself is inside one of these")

	wakesPerAddr := fs.Int("quota-wakes-per-address", 20, "max wakes per push address per window (PG-Q-1)")
	wakesPerAddrWindow := fs.Duration("quota-wakes-per-address-window", 5*time.Minute, "window for -quota-wakes-per-address")
	wakesPerSrc := fs.Int("quota-wakes-per-source", 600, "max wakes per source IP per window (PG-Q-1)")
	wakesPerSrcWindow := fs.Duration("quota-wakes-per-source-window", time.Hour, "window for -quota-wakes-per-source")
	regsPerSrc := fs.Int("quota-registrations-per-source", 10, "max registrations per source IP per window (PG-Q-3)")
	regsPerSrcWindow := fs.Duration("quota-registrations-per-source-window", time.Hour, "window for -quota-registrations-per-source")
	regsGlobal := fs.Int("quota-registrations-global", 2000, "max registrations gateway-wide per window (PG-Q-3)")
	regsGlobalWindow := fs.Duration("quota-registrations-global-window", time.Hour, "window for -quota-registrations-global")
	allocsPerSrc := fs.Int("quota-allocations-per-source", 40, "max address allocations per source IP per window (PG-Q-3)")
	allocsPerSrcWindow := fs.Duration("quota-allocations-per-source-window", time.Hour, "window for -quota-allocations-per-source")
	allocsGlobal := fs.Int("quota-allocations-global", 4000, "max address allocations gateway-wide per window (PG-Q-3)")
	allocsGlobalWindow := fs.Duration("quota-allocations-global-window", time.Hour, "window for -quota-allocations-global")
	allocsPerInst := fs.Int("quota-allocations-per-installation", 20, "max live address allocations per installation (PG-ALLOC-4)")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dbPath == "" {
		return errors.New("swarm-pushgw: -db is required")
	}
	if *retentionInterval <= 0 {
		return errors.New("swarm-pushgw: -retention-interval must be positive")
	}
	if *adminListen != "" {
		loopback, err := isLoopbackHostPort(*adminListen)
		if err != nil {
			return fmt.Errorf("swarm-pushgw: -admin-listen: %w", err)
		}
		if !loopback {
			return errors.New("swarm-pushgw: -admin-listen must be loopback-only")
		}
	}
	if (*tlsCert == "") != (*tlsKey == "") {
		return errors.New("swarm-pushgw: -tls-cert and -tls-key must both be set")
	}
	tlsConfigured := *tlsCert != ""
	if !tlsConfigured && !*insecureHTTP {
		return errors.New("swarm-pushgw: TLS is required (PG-TR-1): pass -tls-cert and -tls-key, or set -insecure-http to declare a TLS-terminating reverse proxy deployment")
	}
	if tlsConfigured && *insecureHTTP {
		return errors.New("swarm-pushgw: -tls-cert/-tls-key and -insecure-http are mutually exclusive")
	}

	var trustedProxyCIDRs []string
	if *trustedProxies != "" {
		trustedProxyCIDRs = strings.Split(*trustedProxies, ",")
	}

	deps := productionDependencies{
		sender:   devSender{},
		attestor: notImplementedAttestor{},
	}
	if *devMode {
		if *googleCredentials != "" {
			return errors.New("swarm-pushgw: -dev and -google-credentials are mutually exclusive")
		}
	} else {
		production, buildErr := buildProductionDependencies(ctx, productionDependencyConfig{
			CredentialPath:        *googleCredentials,
			ProjectID:             *gcpProjectID,
			ProjectNumber:         *gcpProjectNumber,
			PackageName:           *androidPackage,
			SigningCertificateSHA: *playSigningCert,
		}, loadRuntimeGoogleCredentials)
		if buildErr != nil {
			return buildErr
		}
		deps = production
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	srv, err := pushgw.NewServer(pushgw.Config{
		DBPath:         *dbPath,
		Sender:         deps.sender,
		Attest:         deps.attestor,
		Logger:         logger,
		TrustedProxies: trustedProxyCIDRs,
		Readiness: pushgw.DeploymentReadiness{
			ProductionSender:   deps.readiness.ProductionSender,
			ProductionAttestor: deps.readiness.ProductionAttestor,
			RequiredConfig:     deps.readiness.RequiredConfig,
			RetentionFreshFor:  2 * *retentionInterval,
		},
		Quotas: pushgw.QuotaConfig{
			WakesPerAddress:            pushgw.RateLimit{Max: *wakesPerAddr, Window: *wakesPerAddrWindow},
			WakesPerSourceIP:           pushgw.RateLimit{Max: *wakesPerSrc, Window: *wakesPerSrcWindow},
			RegistrationsPerSourceIP:   pushgw.RateLimit{Max: *regsPerSrc, Window: *regsPerSrcWindow},
			RegistrationsGlobal:        pushgw.RateLimit{Max: *regsGlobal, Window: *regsGlobalWindow},
			AllocationsPerSourceIP:     pushgw.RateLimit{Max: *allocsPerSrc, Window: *allocsPerSrcWindow},
			AllocationsGlobal:          pushgw.RateLimit{Max: *allocsGlobal, Window: *allocsGlobalWindow},
			AllocationsPerInstallation: *allocsPerInst,
		},
	})
	if err != nil {
		return err
	}
	defer func() { _ = srv.Close() }()

	// Establish one successful retention pass before any listener can become ready;
	// the worker owns its own running signal after this point.
	if err := srv.RunRetention(ctx); err != nil {
		return fmt.Errorf("swarm-pushgw: initial retention sweep: %w", err)
	}
	stopRetention := startRetentionWorker(ctx, srv, *retentionInterval, logger)
	defer stopRetention()

	var adminLn net.Listener
	var adminSrv *http.Server
	if *adminListen != "" {
		adminLn, err = net.Listen("tcp", *adminListen)
		if err != nil {
			return fmt.Errorf("swarm-pushgw: admin listener: %w", err)
		}
		defer func() { _ = adminLn.Close() }()
		adminSrv = newHTTPServer(*adminListen, srv.AdminHandler())
	}

	publicLn, err := net.Listen("tcp", *listen)
	if err != nil {
		return fmt.Errorf("swarm-pushgw: public listener: %w", err)
	}
	defer func() { _ = publicLn.Close() }()

	httpSrv := newHTTPServer(*listen, srv)
	errCh := make(chan error, 2)
	if tlsConfigured {
		httpSrv.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS13}
		cert, certErr := tls.LoadX509KeyPair(*tlsCert, *tlsKey)
		if certErr != nil {
			return fmt.Errorf("swarm-pushgw: load TLS keypair: %w", certErr)
		}
		httpSrv.TLSConfig.Certificates = []tls.Certificate{cert}
		publicLn = tls.NewListener(publicLn, httpSrv.TLSConfig)
	} else {
		httpSrv.TLSConfig = nil
	}
	srv.SetServing(true)
	defer srv.SetServing(false)
	go func() { errCh <- httpSrv.Serve(publicLn) }()
	if adminSrv != nil {
		go func() { errCh <- adminSrv.Serve(adminLn) }()
	}

	select {
	case <-ctx.Done():
		// Readiness drops before connection draining begins, and the retention
		// worker is joined before the deferred store close can run.
		srv.SetServing(false)
		stopRetention()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		var errs []error
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			errs = append(errs, err)
		}
		if adminSrv != nil {
			if err := adminSrv.Shutdown(shutdownCtx); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	case err := <-errCh:
		srv.SetServing(false)
		stopRetention()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
		if adminSrv != nil {
			_ = adminSrv.Shutdown(shutdownCtx)
		}
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// startRetentionWorker returns an idempotent synchronous stop function. The worker owns
// its running signal, and every caller that stops it joins it before the store can close.
func startRetentionWorker(ctx context.Context, srv *pushgw.Server, interval time.Duration, logger *slog.Logger) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	var once sync.Once
	go func() {
		defer close(done)
		runRetentionLoop(ctx, srv, interval, stop, logger)
	}()
	return func() {
		once.Do(func() { close(stop) })
		<-done
	}
}

func isLoopbackHostPort(addr string) (bool, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false, err
	}
	if host == "localhost" {
		return true, nil
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false, fmt.Errorf("host %q is not an IP address or localhost", host)
	}
	return ip.IsLoopback(), nil
}

// runRetentionLoop drives Server.RunRetention on a real-clock ticker (the fake-clock seam
// is Config.Now, exercised by the test suite; production always uses the wall clock).
func runRetentionLoop(ctx context.Context, srv *pushgw.Server, interval time.Duration, stop <-chan struct{}, logger *slog.Logger) {
	srv.SetRetentionWorkerRunning(true)
	defer srv.SetRetentionWorkerRunning(false)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-t.C:
			if err := srv.RunRetention(ctx); err != nil {
				if logger != nil {
					logger.Error("pushgw retention sweep failed", "error", err)
				}
			}
		}
	}
}

func loadRuntimeGoogleCredentials(ctx context.Context, path string, scopes []string) (runtimeGoogleCredentials, error) {
	params := google.CredentialsParams{Scopes: append([]string(nil), scopes...)}
	var (
		creds *google.Credentials
		err   error
	)
	if path == "" {
		creds, err = google.FindDefaultCredentialsWithParams(ctx, params)
	} else {
		doc, readErr := os.ReadFile(path)
		if readErr != nil {
			return runtimeGoogleCredentials{}, fmt.Errorf("swarm-pushgw: read Google runtime credential: %w", readErr)
		}
		creds, err = google.CredentialsFromJSONWithTypeAndParams(ctx, doc, google.ServiceAccount, params)
	}
	if err != nil {
		return runtimeGoogleCredentials{}, fmt.Errorf("swarm-pushgw: load Google runtime credential: %w", err)
	}
	if creds == nil || creds.TokenSource == nil {
		return runtimeGoogleCredentials{}, errors.New("swarm-pushgw: Google runtime credential has no token source")
	}
	return runtimeGoogleCredentials{ProjectID: creds.ProjectID, TokenSource: creds.TokenSource}, nil
}

func buildProductionDependencies(ctx context.Context, cfg productionDependencyConfig, loader runtimeCredentialLoader) (productionDependencies, error) {
	if cfg.ProjectID != productionGCPProjectID {
		return productionDependencies{}, fmt.Errorf("swarm-pushgw: gcp project id %q is not production project %q", cfg.ProjectID, productionGCPProjectID)
	}
	if cfg.ProjectNumber != pushgw.ProductionCloudProjectNumber {
		return productionDependencies{}, fmt.Errorf("swarm-pushgw: gcp project number %d is not production project number %d", cfg.ProjectNumber, pushgw.ProductionCloudProjectNumber)
	}
	if cfg.PackageName != pushgw.ProductionAndroidPackage {
		return productionDependencies{}, fmt.Errorf("swarm-pushgw: Android package %q is not production package %q", cfg.PackageName, pushgw.ProductionAndroidPackage)
	}
	if cfg.SigningCertificateSHA != productionPlaySigningCertificateSHA256 {
		return productionDependencies{}, errors.New("swarm-pushgw: Play signing certificate is not the active production Play App Signing certificate")
	}
	if loader == nil {
		return productionDependencies{}, errors.New("swarm-pushgw: Google runtime credential loader is required")
	}
	creds, err := loader(ctx, cfg.CredentialPath, []string{fcmMessagingScope, playIntegrityScope})
	if err != nil {
		return productionDependencies{}, err
	}
	if creds.ProjectID != productionGCPProjectID {
		return productionDependencies{}, fmt.Errorf("swarm-pushgw: runtime credential project %q is not %q", creds.ProjectID, productionGCPProjectID)
	}
	if creds.TokenSource == nil {
		return productionDependencies{}, errors.New("swarm-pushgw: runtime credential token source is missing")
	}
	hc := oauth2.NewClient(ctx, creds.TokenSource)
	hc.Timeout = productionProviderTimeout
	fcm, err := push.NewFCM(push.FCMConfig{
		ProjectID: cfg.ProjectID, AuthorizedHTTPClient: hc, RetryDelay: push.DefaultRetryDelay,
	})
	if err != nil {
		return productionDependencies{}, fmt.Errorf("swarm-pushgw: construct FCM sender: %w", err)
	}
	decoder, err := pushgw.NewGooglePlayIntegrityDecodeClient(hc)
	if err != nil {
		return productionDependencies{}, err
	}
	attestor, err := pushgw.NewPlayIntegrityVerifier(pushgw.PlayIntegrityConfig{
		PackageName:              cfg.PackageName,
		AllowedCertificateSHA256: []string{cfg.SigningCertificateSHA},
		MaxVerdictAge:            productionPlayVerdictMaxAge,
		MaxFutureSkew:            productionPlayVerdictMaxFutureSkew,
		Now:                      time.Now,
		Decode:                   decoder,
	})
	if err != nil {
		return productionDependencies{}, err
	}
	return productionDependencies{
		sender:   pushgw.NewFCMSender(fcm),
		attestor: attestor,
		readiness: pushgw.DeploymentReadiness{
			ProductionSender: true, ProductionAttestor: true, RequiredConfig: true,
		},
	}, nil
}

// devSender is the no-credential dev-mode WakeSender.
type devSender struct{}

func (devSender) Send(context.Context, string, []byte) error { return pushgw.ErrUnavailable }

// notImplementedAttestor is explicit -dev mode only. It fails every registration closed
// (attestation_unavailable, retryable) rather than fabricate a verdict; production mode
// constructs the real Google decoder and strict Play verifier before it can become ready.
type notImplementedAttestor struct{}

func (notImplementedAttestor) Verify(context.Context, string) (pushgw.VerdictBinding, error) {
	return pushgw.VerdictBinding{}, pushgw.ErrAttestationUnavailable
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
