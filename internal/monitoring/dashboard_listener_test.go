package monitoring

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/timeflareio/guardian/internal/config"
	"github.com/timeflareio/guardian/internal/dashboard"
)

// The listener wiring, end to end over a real socket. Unit-testing the handler
// proves the JSON; this proves the third listener is actually bound, on the
// address the config asked for, and that shutdown takes it down with the others.

// fakeSource is a minimal dashboard.Source.
type fakeSource struct{}

// listenerReadyTimeout bounds how long a test waits for a freshly started
// listener to accept a connection. It is deliberately generous.
//
// These are readiness polls, not latency assertions: the loop breaks on the
// first success, so a longer bound costs a passing test nothing and only changes
// how long a genuinely broken listener takes to report. The previous 3s was fine
// on a developer machine and marginal on a loaded CI runner under -race, where it
// failed a release: the TLS dashboard test timed out at 3.05s having never been
// given a chance to bind.
const listenerReadyTimeout = 30 * time.Second

func (fakeSource) Vitals(context.Context) dashboard.Vitals {
	return dashboard.Vitals{ChainID: "timeflare-test", Running: true}
}
func (fakeSource) Assignments(context.Context) dashboard.Assignments {
	return dashboard.Assignments{Active: []dashboard.Assignment{}}
}
func (fakeSource) Economics(context.Context) dashboard.Economics { return dashboard.Economics{} }
func (fakeSource) Keys(context.Context) dashboard.Keys           { return dashboard.Keys{} }
func (fakeSource) Config(context.Context) dashboard.Config       { return dashboard.Config{} }
func (fakeSource) Activity(context.Context) dashboard.Activity   { return dashboard.Activity{} }

// freePorts reserves n ports and releases them, so the test binds real but
// unpredictable ports instead of racing whatever is on the defaults.
func freePorts(t *testing.T, n int) []int {
	t.Helper()
	ports := make([]int, 0, n)
	for i := 0; i < n; i++ {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("reserving a port: %v", err)
		}
		ports = append(ports, ln.Addr().(*net.TCPAddr).Port)
		_ = ln.Close()
	}
	return ports
}

func dashboardTestConfig(t *testing.T) *config.Config {
	t.Helper()
	ports := freePorts(t, 3)
	cfg := config.DefaultConfig()
	cfg.BindAddress = "127.0.0.1"
	cfg.HealthPort = ports[0]
	cfg.MetricsPort = ports[1]
	cfg.DashboardPort = ports[2]
	cfg.EnableDashboard = true
	cfg.ShutdownTimeout = 2 * time.Second
	return cfg
}

// startService runs the monitoring service and waits for the dashboard to answer.
func startService(t *testing.T, cfg *config.Config, src dashboard.Source) (*Service, func()) {
	t.Helper()
	svc := NewService(cfg, zap.NewNop())
	if src != nil {
		svc.SetDashboardSource(src)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- svc.Start(ctx) }()

	// Bind is synchronous inside Start, but Serve is not — poll briefly.
	deadline := time.Now().Add(listenerReadyTimeout)
	for time.Now().Before(deadline) {
		select {
		case err := <-errCh:
			cancel()
			t.Fatalf("monitoring service failed to start: %v", err)
		default:
		}
		if conn, err := net.DialTimeout("tcp",
			fmt.Sprintf("127.0.0.1:%d", cfg.DashboardPort), 100*time.Millisecond); err == nil {
			_ = conn.Close()
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	return svc, func() {
		cancel()
		<-errCh
	}
}

func TestDashboardListenerServesOverTheSocket(t *testing.T) {
	cfg := dashboardTestConfig(t)
	_, stop := startService(t, cfg, fakeSource{})
	defer stop()

	res, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/api/vitals", cfg.DashboardPort))
	if err != nil {
		t.Fatalf("dashboard did not answer: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("want 200 from the dashboard, got %d", res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("dashboard served invalid JSON: %v", err)
	}
	if got["chain_id"] != "timeflare-test" {
		t.Errorf("the Source's data did not reach the response: %v", got["chain_id"])
	}
}

func TestDashboardFollowsTheSharedBindAddress(t *testing.T) {
	// The dashboard has no bind address of its own: it comes up wherever health
	// and metrics do. Bound to loopback here so the test does not open a port to
	// the network, but the point is that it read BindAddress at all.
	cfg := dashboardTestConfig(t)
	cfg.BindAddress = "127.0.0.1"
	_, stop := startService(t, cfg, fakeSource{})
	defer stop()

	res, err := http.Get(fmt.Sprintf("http://%s:%d/api/vitals", cfg.BindAddress, cfg.DashboardPort))
	if err != nil {
		t.Fatalf("dashboard should be bound on the shared address: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Errorf("want 200 on the shared bind address, got %d", res.StatusCode)
	}
}

func TestDashboardNotBoundWithoutASource(t *testing.T) {
	// A dashboard with nothing to show would be worse than none: the listener
	// only comes up when a Source was supplied.
	cfg := dashboardTestConfig(t)
	svc := NewService(cfg, zap.NewNop())
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
		t.Error("the dashboard port must not be bound when no Source was set")
	}
}

func TestDashboardDisabledByConfig(t *testing.T) {
	cfg := dashboardTestConfig(t)
	cfg.EnableDashboard = false
	svc := NewService(cfg, zap.NewNop())
	svc.SetDashboardSource(fakeSource{})
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- svc.Start(ctx) }()
	defer func() {
		cancel()
		<-errCh
	}()

	time.Sleep(300 * time.Millisecond)
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", cfg.DashboardPort), 200*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		t.Error("enable_dashboard=false must leave the port unbound")
	}
}

func TestTakenDashboardPortFailsStartupLoudly(t *testing.T) {
	// Same discipline as the metrics and health listeners: a taken port fails
	// startup rather than leaving the operator with a silently missing
	// dashboard.
	cfg := dashboardTestConfig(t)
	squatter, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", cfg.DashboardPort))
	if err != nil {
		t.Fatalf("could not occupy the dashboard port: %v", err)
	}
	defer func() { _ = squatter.Close() }()

	svc := NewService(cfg, zap.NewNop())
	svc.SetDashboardSource(fakeSource{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = svc.Start(ctx)
	if err == nil {
		t.Fatal("a taken dashboard port must fail startup")
	}
	if !strings.Contains(err.Error(), "dashboard listener failed to bind") {
		t.Errorf("the error should name the dashboard listener, got %q", err.Error())
	}
}

func TestShutdownClosesTheDashboardListener(t *testing.T) {
	cfg := dashboardTestConfig(t)
	_, stop := startService(t, cfg, fakeSource{})
	stop()

	// After shutdown the port must be free — a listener left behind would block
	// the next start with a confusing bind error.
	deadline := time.Now().Add(listenerReadyTimeout)
	for time.Now().Before(deadline) {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", cfg.DashboardPort))
		if err == nil {
			_ = ln.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Error("the dashboard port was still bound after shutdown")
}
