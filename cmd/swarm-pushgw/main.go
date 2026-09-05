// Command swarm-pushgw boots the Firestore-backed Swarm push gateway
// (docs/specifications/push-gateway-api.md, ADR-027). It fails closed rather than booting
// with an implicit project, namespace, admission set, or encryption key.
//
// PG-TR-1 requires HTTPS on the ordinary Web PKI for every operation, TLS 1.3 as the floor.
// This binary satisfies that in one of two DECLARED ways, never silently: pass -tls-cert
// and -tls-key to terminate TLS in-process (ListenAndServeTLS, MinVersion TLS 1.3), or pass
// -insecure-http to declare that TLS is terminated by a reverse proxy in front of this
// process -- the recorded deployment assumption PG-TR-1's absence of a self-hosted relay's
// certificate-provisioning excuses invites. -insecure-http is a deliberate, explicit flag
// specifically so an operator cannot reach a plaintext listener by omission. A
// TLS-terminating proxy also means every caller presents the proxy's address, not its own
// (PG-Q-4). Production conservatively uses that raw peer for quota accounting until hosted
// forwarding-header evidence exists; -trusted-proxies is available only in explicit dev.
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
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/Nathandela/swarm/internal/pushgw"
	"github.com/Nathandela/swarm/internal/remote/push"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"
)

// shutdownTimeout bounds how long a graceful shutdown waits for in-flight requests.
const shutdownTimeout = 10 * time.Second

const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 15 * time.Second
	writeTimeout      = 30 * time.Second
	idleTimeout       = 60 * time.Second
	maxHeaderBytes    = 32 << 10

	productionGCPProjectID                 = "swarm-8404f"
	productionFirestoreDatabase            = "(default)"
	productionPlaySigningCertificateSHA256 = "hz8YTGhTTgpYccjMiQDrhx5HcddqRsTu1HRcmhhknmU"
	fcmMessagingScope                      = "https://www.googleapis.com/auth/firebase.messaging"
	playIntegrityScope                     = "https://www.googleapis.com/auth/playintegrity"
	datastoreScope                         = "https://www.googleapis.com/auth/datastore"
	productionProviderTimeout              = 15 * time.Second
	productionPlayVerdictMaxAge            = 2 * time.Minute
	productionPlayVerdictMaxFutureSkew     = 30 * time.Second
	retentionTimeout                       = 30 * time.Second
)

var firestoreNamespacePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

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
	sender      pushgw.WakeSender
	attestor    pushgw.AttestationVerifier
	readiness   pushgw.DeploymentReadiness
	tokenSource oauth2.TokenSource
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
		case "healthcheck":
			return runHealthcheck(ctx, args[1:])
		case "retention":
			return runGateway(ctx, args[1:], true)
		default:
			if !strings.HasPrefix(args[0], "-") {
				return fmt.Errorf("swarm-pushgw: unknown command %q", args[0])
			}
		}
	}
	return runServe(ctx, args)
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
	return runGateway(ctx, args, false)
}

func runGateway(ctx context.Context, args []string, retentionOnly bool) error {
	fs := flag.NewFlagSet("swarm-pushgw", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	listen := fs.String("listen", ":8443", "address to serve the gateway on")
	adminListen := fs.String("admin-listen", "", "separate loopback-only address for /healthz, /readyz, and /metrics (empty disables)")
	devMode := fs.Bool("dev", false, "explicit fail-closed development mode: no Google credential, FCM delivery, or Play registration")
	allowFirestoreEmulator := fs.Bool("allow-firestore-emulator", false, "permit FIRESTORE_EMULATOR_HOST; requires -dev")
	googleCredentials := fs.String("google-credentials", "", "path to one swarm-8404f service-account JSON for FCM, Play Integrity, and Firestore (empty uses Application Default Credentials)")
	gcpProjectID := fs.String("gcp-project-id", "", "Google Cloud project id (required; production is fixed)")
	firestoreNamespace := fs.String("firestore-namespace", "", "fresh bounded Firestore collection namespace (required)")
	tokenKeyringFile := fs.String("token-keyring-file", "", "path to the stable versioned token encryption keyring (required)")
	registrationAdmissionFile := fs.String("registration-admission-file", "", "path to the closed-beta installation public-key allowlist (required)")
	gcpProjectNumber := fs.Int64("gcp-project-number", pushgw.ProductionCloudProjectNumber, "Google Cloud project number (production is fixed)")
	androidPackage := fs.String("android-package", pushgw.ProductionAndroidPackage, "Google Play Android package (production is fixed)")
	playSigningCert := fs.String("play-signing-cert-sha256", productionPlaySigningCertificateSHA256, "allowed Play App Signing certificate SHA-256, canonical base64url")

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
	if fs.NArg() != 0 {
		return errors.New("swarm-pushgw: unexpected positional arguments")
	}
	if !retentionOnly {
		explicitListen := false
		fs.Visit(func(f *flag.Flag) { explicitListen = explicitListen || f.Name == "listen" })
		resolvedListen, err := resolveListenAddress(*listen, os.Getenv("PORT"), explicitListen)
		if err != nil {
			return err
		}
		*listen = resolvedListen
	}
	if err := validateFirestoreMode(*devMode, *allowFirestoreEmulator, *gcpProjectID, *firestoreNamespace, os.Getenv("FIRESTORE_EMULATOR_HOST")); err != nil {
		return err
	}
	if !retentionOnly && *adminListen != "" {
		loopback, err := isLoopbackHostPort(*adminListen)
		if err != nil {
			return fmt.Errorf("swarm-pushgw: -admin-listen: %w", err)
		}
		if !loopback {
			return errors.New("swarm-pushgw: -admin-listen must be loopback-only")
		}
	}
	if !retentionOnly && (*tlsCert == "") != (*tlsKey == "") {
		return errors.New("swarm-pushgw: -tls-cert and -tls-key must both be set")
	}
	tlsConfigured := *tlsCert != ""
	if !retentionOnly && !tlsConfigured && !*insecureHTTP {
		return errors.New("swarm-pushgw: TLS is required (PG-TR-1): pass -tls-cert and -tls-key, or set -insecure-http to declare a TLS-terminating reverse proxy deployment")
	}
	if !retentionOnly && tlsConfigured && *insecureHTTP {
		return errors.New("swarm-pushgw: -tls-cert/-tls-key and -insecure-http are mutually exclusive")
	}

	trustedProxyCIDRs, err := validatedTrustedProxies(*devMode, *trustedProxies)
	if err != nil {
		return err
	}
	tokenKeys, activeTokenKeyVersion, registrationDigestKey, err := loadTokenKeyring(*tokenKeyringFile)
	if err != nil {
		return err
	}
	registrationAdmission, err := loadRegistrationAdmission(*registrationAdmissionFile)
	if err != nil {
		return err
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

	clientOptions := []option.ClientOption(nil)
	if !*devMode {
		clientOptions = append(clientOptions, option.WithTokenSource(deps.tokenSource))
	}
	firestoreClient, err := firestore.NewClientWithDatabase(ctx, *gcpProjectID, productionFirestoreDatabase, clientOptions...)
	if err != nil {
		return fmt.Errorf("swarm-pushgw: construct Firestore client: %w", err)
	}
	defer func() { _ = firestoreClient.Close() }()
	repository, err := pushgw.NewFirestoreRepository(firestoreClient, *firestoreNamespace)
	if err != nil {
		return err
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	srv, err := pushgw.NewFirestoreServer(pushgw.Config{
		Repository:            repository,
		TokenKeys:             tokenKeys,
		ActiveTokenKeyVersion: activeTokenKeyVersion,
		RegistrationDigestKey: registrationDigestKey,
		RegistrationAdmission: registrationAdmission,
		Sender:                deps.sender,
		Attest:                deps.attestor,
		Logger:                logger,
		TrustedProxies:        trustedProxyCIDRs,
		Readiness: pushgw.DeploymentReadiness{
			ProductionSender:   deps.readiness.ProductionSender,
			ProductionAttestor: deps.readiness.ProductionAttestor,
			RequiredConfig:     deps.readiness.RequiredConfig,
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

	if retentionOnly {
		retentionCtx, cancel := context.WithTimeout(ctx, retentionTimeout)
		defer cancel()
		if err := srv.RunRetention(retentionCtx); err != nil {
			return fmt.Errorf("swarm-pushgw retention: %w", err)
		}
		return nil
	}
	err = srv.CheckStore(ctx)
	if err != nil {
		return fmt.Errorf("swarm-pushgw: Firestore startup check: %w", err)
	}

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
		// Readiness drops before connection draining begins.
		srv.SetServing(false)
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

func resolveListenAddress(flagValue, port string, explicitlySet bool) (string, error) {
	if explicitlySet || port == "" {
		return flagValue, nil
	}
	value, err := strconv.Atoi(port)
	if err != nil || value < 1 || value > 65535 || strconv.Itoa(value) != port {
		return "", errors.New("swarm-pushgw: PORT must be a canonical integer from 1 to 65535")
	}
	return ":" + port, nil
}

func validateFirestoreMode(devMode, allowEmulator bool, projectID, namespace, emulatorHost string) error {
	if devMode {
		if !allowEmulator {
			return errors.New("swarm-pushgw: Firestore emulator use in -dev requires explicit -allow-firestore-emulator")
		}
		if emulatorHost == "" {
			return errors.New("swarm-pushgw: -dev requires FIRESTORE_EMULATOR_HOST")
		}
	} else if allowEmulator {
		return errors.New("swarm-pushgw: -allow-firestore-emulator requires -dev")
	} else if emulatorHost != "" {
		return errors.New("swarm-pushgw: Firestore emulator environment is forbidden outside -dev")
	}
	if projectID != productionGCPProjectID {
		return fmt.Errorf("swarm-pushgw: -gcp-project-id must be the production project %q", productionGCPProjectID)
	}
	if !firestoreNamespacePattern.MatchString(namespace) {
		return errors.New("swarm-pushgw: -firestore-namespace must match ^[a-z][a-z0-9-]{0,31}$")
	}
	return nil
}

func validatedTrustedProxies(devMode bool, value string) ([]string, error) {
	if value == "" {
		return nil, nil
	}
	if !devMode {
		return nil, errors.New("swarm-pushgw: -trusted-proxies is disabled until hosted forwarding-header evidence is recorded")
	}
	return strings.Split(value, ","), nil
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
	creds, err := loader(ctx, cfg.CredentialPath, []string{fcmMessagingScope, playIntegrityScope, datastoreScope})
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
		sender:      pushgw.NewFCMSender(fcm),
		attestor:    attestor,
		tokenSource: creds.TokenSource,
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
