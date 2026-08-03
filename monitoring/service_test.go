package monitoring

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/timeflareio/guardian/config"
	"go.uber.org/zap"
)

func testConfig(metricsPort, healthPort int) *config.Config {
	cfg := config.DefaultConfig()
	cfg.MetricsPort = metricsPort
	cfg.HealthPort = healthPort
	cfg.LogLevel = "debug"
	return cfg
}

// markHealthy simulates a live, registered guardian recording into the
// health tracker (health/readiness are real checks now, not stubs).
func markHealthy(s *Service) {
	s.Health().SetChainReachable(true)
	s.Health().SetKeyLoadable(true)
	s.Health().SetRegistered(true)
	s.Health().RecordPoll()
}

// freePort asks the kernel for an available TCP port
func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}
	defer func() { _ = listener.Close() }()
	return listener.Addr().(*net.TCPAddr).Port
}

func TestHandleHealth(t *testing.T) {
	service := NewService(testConfig(9101, 8080), zap.NewNop())

	// A fresh guardian with no recorded state is NOT healthy — the endpoint
	// reflects real dependency state, it never lies with a blanket 200.
	recorder := httptest.NewRecorder()
	service.handleHealth(recorder, httptest.NewRequest(http.MethodGet, "/health", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503 for unproven guardian, got %d", recorder.Code)
	}

	markHealthy(service)

	recorder = httptest.NewRecorder()
	service.handleHealth(recorder, httptest.NewRequest(http.MethodGet, "/health", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", recorder.Code)
	}

	status := &HealthStatus{}
	if err := json.Unmarshal(recorder.Body.Bytes(), status); err != nil {
		t.Fatalf("health response is not valid JSON: %v", err)
	}
	if !status.Healthy() {
		t.Fatalf("expected healthy status, got %q", status.Status)
	}
}

func TestHandleReady(t *testing.T) {
	service := NewService(testConfig(9101, 8080), zap.NewNop())

	// Unregistered / inactive guardians report unready so supervisors restart
	// or hold traffic.
	recorder := httptest.NewRecorder()
	service.handleReady(recorder, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503 for unready guardian, got %d", recorder.Code)
	}

	markHealthy(service)

	recorder = httptest.NewRecorder()
	service.handleReady(recorder, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", recorder.Code)
	}
}

// TestStartServesHealthAndMetricsPorts verifies both configured ports are bound:
// the health endpoint on HealthPort and the metrics endpoint on MetricsPort.
func TestStartServesHealthAndMetricsPorts(t *testing.T) {
	metricsPort := freePort(t)
	healthPort := freePort(t)
	if metricsPort == healthPort {
		t.Skip("kernel returned the same port twice")
	}

	service := NewService(testConfig(metricsPort, healthPort), zap.NewNop())
	markHealthy(service)

	ctx, cancel := context.WithCancel(context.Background())
	serviceDone := make(chan error, 1)
	go func() { serviceDone <- service.Start(ctx) }()

	waitForEndpoint := func(url string) *http.Response {
		t.Helper()
		var lastErr error
		for range 50 {
			resp, err := http.Get(url) //nolint:gosec // local test URL
			if err == nil {
				return resp
			}
			lastErr = err
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("endpoint %s never became available: %v", url, lastErr)
		return nil
	}

	// Health endpoint must respond on the dedicated health port
	resp := waitForEndpoint(fmt.Sprintf("http://127.0.0.1:%d/health", healthPort))
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("health endpoint on health port: expected 200, got %d", resp.StatusCode)
	}

	// Metrics endpoint must respond on the metrics port
	resp = waitForEndpoint(fmt.Sprintf("http://127.0.0.1:%d/metrics", metricsPort))
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("metrics endpoint on metrics port: expected 200, got %d", resp.StatusCode)
	}

	// Shut down and confirm a clean exit
	cancel()
	select {
	case err := <-serviceDone:
		if err != nil {
			t.Fatalf("service shutdown returned error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("service did not shut down within 10s")
	}
}

func TestCheckHealth(t *testing.T) {
	t.Run("healthy guardian", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/health" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"healthy","timestamp":"2026-07-06T00:00:00Z"}`))
		}))
		defer server.Close()

		status, err := CheckHealth(context.Background(), server.URL, 2*time.Second)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !status.Healthy() {
			t.Errorf("expected healthy, got %q", status.Status)
		}
	})

	t.Run("unhealthy guardian returns error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"unhealthy"}`))
		}))
		defer server.Close()

		if _, err := CheckHealth(context.Background(), server.URL, 2*time.Second); err == nil {
			t.Fatal("expected error for non-200 response, got nil")
		}
	})

	t.Run("unreachable endpoint returns error", func(t *testing.T) {
		port := freePort(t)
		url := fmt.Sprintf("http://127.0.0.1:%d", port)
		if _, err := CheckHealth(context.Background(), url, 500*time.Millisecond); err == nil {
			t.Fatal("expected error for unreachable endpoint, got nil")
		}
	})

	t.Run("invalid JSON returns error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`not json`))
		}))
		defer server.Close()

		if _, err := CheckHealth(context.Background(), server.URL, 2*time.Second); err == nil {
			t.Fatal("expected error for invalid JSON, got nil")
		}
	})
}
