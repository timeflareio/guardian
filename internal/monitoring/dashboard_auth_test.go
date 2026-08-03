package monitoring

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"

	"github.com/timeflareio/guardian/internal/config"
	"github.com/timeflareio/guardian/internal/dashboard"
)

// Authentication over real sockets. The handler tests prove the 401; these
// prove the daemon actually wires it — that the credential reaches the
// listener, that TLS serves when configured, and that an exposed dashboard with
// no credential is not bound at all while the guardian carries on.

const listenerTestPassword = "a chosen password"

func listenerTestHash(t *testing.T) string {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(listenerTestPassword), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hashing the test password: %v", err)
	}
	return string(hash)
}

func TestDashboardRefusesUnauthenticatedOverTheSocket(t *testing.T) {
	cfg := dashboardTestConfig(t)
	cfg.KeyName = "guardian-07"
	cfg.DashboardPasswordHash = listenerTestHash(t)
	_, stop := startService(t, cfg, fakeSource{})
	defer stop()

	url := fmt.Sprintf("http://127.0.0.1:%d/api/vitals", cfg.DashboardPort)

	res, err := http.Get(url)
	if err != nil {
		t.Fatalf("dashboard did not answer: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401 without credentials, got %d", res.StatusCode)
	}
	// The realm names this guardian, so a browser's saved-credential list stays
	// legible when an operator runs several.
	if challenge := res.Header.Get("WWW-Authenticate"); challenge == "" ||
		!strings.Contains(challenge, "guardian-07") {
		t.Errorf("the challenge should name the guardian, got %q", challenge)
	}

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("building the authenticated request: %v", err)
	}
	req.SetBasicAuth(dashboard.Username, listenerTestPassword)
	authed, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("authenticated request failed: %v", err)
	}
	defer func() { _ = authed.Body.Close() }()
	if authed.StatusCode != http.StatusOK {
		t.Errorf("want 200 with valid credentials, got %d", authed.StatusCode)
	}

	req.SetBasicAuth(dashboard.Username, "not-the-password")
	wrong, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request with a wrong password failed to send: %v", err)
	}
	defer func() { _ = wrong.Body.Close() }()
	if wrong.StatusCode != http.StatusUnauthorized {
		t.Errorf("want 401 with a wrong password, got %d", wrong.StatusCode)
	}
}

func TestExposedDashboardWithoutCredentialIsNotBound(t *testing.T) {
	// Fail closed: the page is withheld rather than served unprotected. The
	// guardian is unaffected — health and metrics stay up, and so do reveals,
	// because a missed window is slashable and a dashboard is not.
	cfg := dashboardTestConfig(t)
	cfg.BindAddress = "0.0.0.0"
	cfg.DashboardPasswordHash = ""

	svc := NewService(cfg, zap.NewNop())
	svc.SetDashboardSource(fakeSource{})
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- svc.Start(ctx) }()
	defer func() {
		cancel()
		<-errCh
	}()

	// Give the other two listeners time to bind before concluding.
	time.Sleep(300 * time.Millisecond)

	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", cfg.DashboardPort), 200*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		t.Error("an exposed dashboard with no credential must not be bound at all")
	}

	// The listeners that must survive it.
	health, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/health", cfg.HealthPort))
	if err != nil {
		t.Fatalf("health must stay up when the dashboard is withheld: %v", err)
	}
	_ = health.Body.Close()

	metrics, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/metrics", cfg.MetricsPort))
	if err != nil {
		t.Fatalf("metrics must stay up when the dashboard is withheld: %v", err)
	}
	_ = metrics.Body.Close()
	if metrics.StatusCode != http.StatusOK {
		t.Errorf("metrics stays unauthenticated for scrapers: got %d", metrics.StatusCode)
	}
}

func TestLoopbackDashboardServesWithoutCredential(t *testing.T) {
	cfg := dashboardTestConfig(t)
	cfg.BindAddress = "127.0.0.1"
	cfg.DashboardPasswordHash = ""
	_, stop := startService(t, cfg, fakeSource{})
	defer stop()

	res, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/api/vitals", cfg.DashboardPort))
	if err != nil {
		t.Fatalf("loopback dashboard did not answer: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Errorf("loopback serves without a credential, got %d", res.StatusCode)
	}
}

func TestLoopbackHonoursAConfiguredCredential(t *testing.T) {
	// The loopback exemption is permission to serve without a credential, not a
	// rule that discards one the operator set.
	cfg := dashboardTestConfig(t)
	cfg.DashboardPasswordHash = listenerTestHash(t)
	_, stop := startService(t, cfg, fakeSource{})
	defer stop()

	res, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/api/vitals", cfg.DashboardPort))
	if err != nil {
		t.Fatalf("dashboard did not answer: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("a configured credential must be enforced on loopback too, got %d", res.StatusCode)
	}
}

func TestDashboardServesOverTLSWhenConfigured(t *testing.T) {
	cfg := dashboardTestConfig(t)
	cfg.DashboardPasswordHash = listenerTestHash(t)
	cfg.DashboardTLSCertFile, cfg.DashboardTLSKeyFile = writeSelfSignedPair(t)

	svc := NewService(cfg, zap.NewNop())
	svc.SetDashboardSource(fakeSource{})
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- svc.Start(ctx) }()
	defer func() {
		cancel()
		<-errCh
	}()

	// The certificate is self-signed and generated for this test alone, so the
	// client skips verification: what is under test is that the listener speaks
	// TLS at all, not the trust chain, which is the operator's.
	client := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, //nolint:gosec // self-signed test certificate
		Timeout:   3 * time.Second,
	}
	url := fmt.Sprintf("https://127.0.0.1:%d/api/vitals", cfg.DashboardPort)

	var res *http.Response
	var err error
	deadline := time.Now().Add(listenerReadyTimeout)
	for time.Now().Before(deadline) {
		req, buildErr := http.NewRequest(http.MethodGet, url, nil)
		if buildErr != nil {
			t.Fatalf("building the request: %v", buildErr)
		}
		req.SetBasicAuth(dashboard.Username, listenerTestPassword)
		if res, err = client.Do(req); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("dashboard did not answer over TLS: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Errorf("want 200 over TLS with valid credentials, got %d", res.StatusCode)
	}

	// Plain HTTP against a TLS listener must not somehow succeed.
	if plain, plainErr := http.Get(fmt.Sprintf("http://127.0.0.1:%d/api/vitals", cfg.DashboardPort)); plainErr == nil {
		_ = plain.Body.Close()
		if plain.StatusCode == http.StatusOK {
			t.Error("a TLS dashboard must not serve plaintext HTTP")
		}
	}
}

func TestWithheldDashboardIsReportedByConfigHelpers(t *testing.T) {
	// The daemon and `config doctor` read the same helper, so the state an
	// operator is told about is the state the listener acts on.
	cfg := config.DefaultConfig()
	cfg.BindAddress = "0.0.0.0"
	cfg.DashboardPasswordHash = ""
	if !cfg.DashboardWithheld() {
		t.Fatal("an exposed dashboard with no credential must report as withheld")
	}
	cfg.DashboardPasswordHash = listenerTestHash(t)
	if cfg.DashboardWithheld() {
		t.Error("a credentialed dashboard must not be withheld")
	}
}

// writeSelfSignedPair generates a certificate and key for the TLS test.
func writeSelfSignedPair(t *testing.T) (certPath, keyPath string) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating a test key: %v", err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "timeflare-guardian-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:         true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("creating a test certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshalling the test key: %v", err)
	}

	dir := t.TempDir()
	certPath = filepath.Join(dir, "dashboard.crt")
	keyPath = filepath.Join(dir, "dashboard.key")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatalf("writing the test certificate: %v", err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatalf("writing the test key: %v", err)
	}
	return certPath, keyPath
}
