package monitoring

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// HealthStatus is the response payload of the guardian /health endpoint
type HealthStatus struct {
	Status    string `json:"status"`
	Timestamp string `json:"timestamp"`
}

// Healthy reports whether the status indicates a healthy guardian
func (h *HealthStatus) Healthy() bool {
	return h.Status == "healthy"
}

// CheckHealth queries a guardian health endpoint and returns its status.
// baseURL is the scheme://host:port of the health server (no path).
func CheckHealth(ctx context.Context, baseURL string, timeout time.Duration) (*HealthStatus, error) {
	client := &http.Client{Timeout: timeout}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/health", nil)
	if err != nil {
		return nil, fmt.Errorf("invalid health URL: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("guardian health endpoint unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return nil, fmt.Errorf("failed to read health response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("guardian reported unhealthy (HTTP %d): %s", resp.StatusCode, string(body))
	}

	status := &HealthStatus{}
	if err := json.Unmarshal(body, status); err != nil {
		return nil, fmt.Errorf("invalid health response %q: %w", string(body), err)
	}

	return status, nil
}
