package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/aether-arb/aether/internal/metrics"
)

// MetricsClient fetches executor metrics over HTTP.
type MetricsClient struct {
	baseURL    string
	httpClient *http.Client
}

// NewMetricsClient creates a client for the executor /metrics/json endpoint.
func NewMetricsClient(url string) *MetricsClient {
	return &MetricsClient{
		baseURL: url,
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

// FetchSnapshot returns the current executor metrics snapshot.
func (c *MetricsClient) FetchSnapshot(ctx context.Context) (metrics.Snapshot, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL, nil)
	if err != nil {
		return metrics.Snapshot{}, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return metrics.Snapshot{}, fmt.Errorf("executor unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return metrics.Snapshot{}, fmt.Errorf("executor returned %d: %s", resp.StatusCode, body)
	}
	var snap metrics.Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		return metrics.Snapshot{}, fmt.Errorf("decode metrics: %w", err)
	}
	snap.ExecutorReachable = true
	return snap, nil
}

// AdminClient calls executor admin endpoints.
type AdminClient struct {
	baseHost   string
	httpClient *http.Client
	adminToken string
}

// NewAdminClient creates a client for executor admin endpoints.
// metricsURL is e.g. http://localhost:8080/metrics/json — host is derived from it.
func NewAdminClient(metricsURL string) *AdminClient {
	host := metricsURL
	if idx := len(metricsURL) - len("/metrics/json"); idx > 0 && metricsURL[idx:] == "/metrics/json" {
		host = metricsURL[:idx]
	}
	return &AdminClient{
		baseHost:   host,
		httpClient: &http.Client{Timeout: 5 * time.Second},
		adminToken: os.Getenv("AETHER_ADMIN_TOKEN"),
	}
}

func (a *AdminClient) post(ctx context.Context, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseHost+path, nil)
	if err != nil {
		return err
	}
	if a.adminToken != "" {
		req.Header.Set("Authorization", "Bearer "+a.adminToken)
	}
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("admin %s: %d %s", path, resp.StatusCode, body)
	}
	return nil
}

// Pause stops bundle submission.
func (a *AdminClient) Pause(ctx context.Context) error {
	return a.post(ctx, "/admin/pause")
}

// Resume resumes bundle submission.
func (a *AdminClient) Resume(ctx context.Context) error {
	return a.post(ctx, "/admin/resume")
}

// SetMinProfit updates the minimum profit threshold.
func (a *AdminClient) SetMinProfit(ctx context.Context, value float64) error {
	return a.post(ctx, fmt.Sprintf("/admin/set_min_profit?value=%g", value))
}
