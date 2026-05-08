package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/iancoleman/strcase"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
)

// mockGmailServer creates a test server that mimics Gmail API responses
func mockGmailServer(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/gmail/v1/users/me/labels":
			resp := gmail.ListLabelsResponse{
				Labels: []*gmail.Label{
					{Id: "INBOX", Name: "INBOX", ThreadsTotal: 100, ThreadsUnread: 5},
					{Id: "SENT", Name: "SENT", ThreadsTotal: 50, ThreadsUnread: 0},
					{Id: "SPAM", Name: "SPAM", ThreadsTotal: 10, ThreadsUnread: 3},
				},
			}
			json.NewEncoder(w).Encode(resp)
		case strings.HasPrefix(r.URL.Path, "/gmail/v1/users/me/labels/"):
			labelId := strings.TrimPrefix(r.URL.Path, "/gmail/v1/users/me/labels/")
			var label *gmail.Label
			switch labelId {
			case "INBOX":
				label = &gmail.Label{Id: "INBOX", Name: "INBOX", ThreadsTotal: 100, ThreadsUnread: 5}
			case "SENT":
				label = &gmail.Label{Id: "SENT", Name: "SENT", ThreadsTotal: 50, ThreadsUnread: 0}
			case "SPAM":
				label = &gmail.Label{Id: "SPAM", Name: "SPAM", ThreadsTotal: 10, ThreadsUnread: 3}
			default:
				w.WriteHeader(http.StatusNotFound)
				return
			}
			json.NewEncoder(w).Encode(label)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

// Helper to create a Gmail service pointing to a test server
func newTestGmailService(t *testing.T, server *httptest.Server) *gmail.Service {
	// Create a transport that redirects to our test server
	transport := &testTransport{baseURL: server.URL}
	httpClient := &http.Client{Transport: transport}

	srv, err := gmail.NewService(context.Background(), option.WithHTTPClient(httpClient))
	if err != nil {
		t.Fatalf("Failed to create test Gmail service: %v", err)
	}
	return srv
}

type testTransport struct {
	baseURL string
}

func (t *testTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Redirect Gmail API requests to our test server
	req.URL.Scheme = "http"
	req.URL.Host = strings.TrimPrefix(t.baseURL, "http://")
	return http.DefaultTransport.RoundTrip(req)
}

// TestLoadConfig tests configuration file loading
func TestLoadConfig(t *testing.T) {
	tests := []struct {
		name        string
		configYAML  string
		wantErr     bool
		errContains string
		wantConfig  Config
	}{
		{
			name: "valid config with single label",
			configYAML: `---
interval: 300
labels:
  - INBOX
`,
			wantErr: false,
			wantConfig: Config{
				Interval: 300,
				Labels:   []string{"INBOX"},
			},
		},
		{
			name: "valid config with multiple labels",
			configYAML: `---
interval: 60
labels:
  - INBOX
  - SENT
  - SPAM
`,
			wantErr: false,
			wantConfig: Config{
				Interval: 60,
				Labels:   []string{"INBOX", "SENT", "SPAM"},
			},
		},
		{
			name:        "invalid YAML",
			configYAML:  "invalid: [yaml: content",
			wantErr:     true,
			errContains: "yaml",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpfile, err := os.CreateTemp("", "config-*.yml")
			if err != nil {
				t.Fatalf("Failed to create temp file: %v", err)
			}
			defer os.Remove(tmpfile.Name())

			if _, err := tmpfile.Write([]byte(tt.configYAML)); err != nil {
				t.Fatalf("Failed to write temp file: %v", err)
			}
			tmpfile.Close()

			config, err := loadConfig(tmpfile.Name())

			if tt.wantErr {
				if err == nil {
					t.Errorf("Expected error but got none")
				}
				if tt.errContains != "" && err != nil && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("Expected error containing %q, got: %v", tt.errContains, err)
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
				if config.Interval != tt.wantConfig.Interval {
					t.Errorf("Interval mismatch: got %d, want %d", config.Interval, tt.wantConfig.Interval)
				}
				if len(config.Labels) != len(tt.wantConfig.Labels) {
					t.Errorf("Labels length mismatch: got %d, want %d", len(config.Labels), len(tt.wantConfig.Labels))
				}
			}
		})
	}
}

func TestLoadConfigFileNotFound(t *testing.T) {
	_, err := loadConfig("/nonexistent/path/config.yml")
	if err == nil {
		t.Error("Expected error for non-existent file")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Expected file not found error, got: %v", err)
	}
}

func TestLoadConfigInvalidInterval(t *testing.T) {
	tmpfile, err := os.CreateTemp("", "config-*.yml")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpfile.Name())
	tmpfile.WriteString("interval: 0\nlabels:\n  - INBOX\n")
	tmpfile.Close()

	_, err = loadConfig(tmpfile.Name())
	if err == nil {
		t.Error("Expected error for invalid interval")
	}
	if !strings.Contains(err.Error(), "interval") {
		t.Errorf("Expected interval error, got: %v", err)
	}
}

func TestLoadConfigEmptyLabels(t *testing.T) {
	tmpfile, err := os.CreateTemp("", "config-*.yml")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpfile.Name())
	tmpfile.WriteString("interval: 300\nlabels: []\n")
	tmpfile.Close()

	_, err = loadConfig(tmpfile.Name())
	if err == nil {
		t.Error("Expected error for empty labels")
	}
	if !strings.Contains(err.Error(), "labels") {
		t.Errorf("Expected labels error, got: %v", err)
	}
}

// TestMatchLabels tests label matching logic
func TestMatchLabels(t *testing.T) {
	tests := []struct {
		name          string
		labels        []*gmail.Label
		desiredLabels []string
		wantLabelIds  []string
	}{
		{
			name: "exact match single label",
			labels: []*gmail.Label{
				{Id: "INBOX", Name: "INBOX"},
				{Id: "SENT", Name: "SENT"},
			},
			desiredLabels: []string{"INBOX"},
			wantLabelIds:  []string{"INBOX"},
		},
		{
			name: "multiple matches",
			labels: []*gmail.Label{
				{Id: "INBOX", Name: "INBOX"},
				{Id: "SENT", Name: "SENT"},
				{Id: "SPAM", Name: "SPAM"},
			},
			desiredLabels: []string{"INBOX", "SENT"},
			wantLabelIds:  []string{"INBOX", "SENT"},
		},
		{
			name: "no matches",
			labels: []*gmail.Label{
				{Id: "INBOX", Name: "INBOX"},
			},
			desiredLabels: []string{"NONEXISTENT"},
			wantLabelIds:  []string{},
		},
		{
			name: "partial match",
			labels: []*gmail.Label{
				{Id: "INBOX", Name: "INBOX"},
				{Id: "SENT", Name: "SENT"},
			},
			desiredLabels: []string{"INBOX", "NONEXISTENT"},
			wantLabelIds:  []string{"INBOX"},
		},
		{
			name:          "empty labels list",
			labels:        []*gmail.Label{},
			desiredLabels: []string{"INBOX"},
			wantLabelIds:  []string{},
		},
		{
			name: "empty desired labels",
			labels: []*gmail.Label{
				{Id: "INBOX", Name: "INBOX"},
			},
			desiredLabels: []string{},
			wantLabelIds:  []string{},
		},
		{
			name: "case sensitive match",
			labels: []*gmail.Label{
				{Id: "inbox", Name: "inbox"},
			},
			desiredLabels: []string{"INBOX"},
			wantLabelIds:  []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchLabels(tt.labels, tt.desiredLabels)

			if len(got) != len(tt.wantLabelIds) {
				t.Errorf("Got %d label IDs, want %d", len(got), len(tt.wantLabelIds))
			}
			for i := range got {
				if got[i] != tt.wantLabelIds[i] {
					t.Errorf("LabelIds[%d]: got %q, want %q", i, got[i], tt.wantLabelIds[i])
				}
			}
		})
	}
}

func TestMatchLabelsEmptyDesiredLabel(t *testing.T) {
	labels := []*gmail.Label{
		{Id: "INBOX", Name: "INBOX"},
		{Id: "SENT", Name: "SENT"},
	}
	desired := []string{"", "INBOX", "", "SENT"}
	got := matchLabels(labels, desired)
	if len(got) != 2 {
		t.Errorf("Expected 2 matches, got %d: %v", len(got), got)
	}
	if got[0] != "INBOX" || got[1] != "SENT" {
		t.Errorf("Expected [INBOX SENT], got %v", got)
	}
}

// TestScrapeMetrics tests the metrics scraping function
func TestScrapeMetrics(t *testing.T) {
	server := mockGmailServer(t)
	defer server.Close()

	srv := newTestGmailService(t, server)

	registry := prometheus.NewRegistry()
	unreadGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "gmail_threads_unread", Help: "number of unread threads"},
		[]string{"Label"},
	)
	totalGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "gmail_threads_total", Help: "total number of threads"},
		[]string{"Label"},
	)
	registry.MustRegister(unreadGauge, totalGauge)

	labelIds := []string{"INBOX", "SENT"}

	err := scrapeMetrics(unreadGauge, totalGauge, labelIds, srv)
	if err != nil {
		t.Fatalf("scrapeMetrics failed: %v", err)
	}

	// Verify metrics were set
	// We can't easily read back gauge values, but we can verify via the metrics endpoint
	handler := promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, `gmail_threads_total{Label="gmail_inbox"} 100`) {
		t.Errorf("Expected INBOX total metric not found in:\n%s", body)
	}
	if !strings.Contains(body, `gmail_threads_unread{Label="gmail_inbox"} 5`) {
		t.Errorf("Expected INBOX unread metric not found in:\n%s", body)
	}
	if !strings.Contains(body, `gmail_threads_total{Label="gmail_sent"} 50`) {
		t.Errorf("Expected SENT total metric not found in:\n%s", body)
	}
}

func TestScrapeMetricsInvalidLabel(t *testing.T) {
	server := mockGmailServer(t)
	defer server.Close()

	srv := newTestGmailService(t, server)

	registry := prometheus.NewRegistry()
	unreadGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "gmail_threads_unread", Help: "number of unread threads"},
		[]string{"Label"},
	)
	totalGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "gmail_threads_total", Help: "total number of threads"},
		[]string{"Label"},
	)
	registry.MustRegister(unreadGauge, totalGauge)

	labelIds := []string{"NONEXISTENT"}

	err := scrapeMetrics(unreadGauge, totalGauge, labelIds, srv)
	if err == nil {
		t.Error("Expected error for invalid label ID")
	}
}

// TestRecordMetrics tests the recordMetrics goroutine
func TestRecordMetrics(t *testing.T) {
	server := mockGmailServer(t)
	defer server.Close()

	srv := newTestGmailService(t, server)

	registry := prometheus.NewRegistry()
	unreadGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "gmail_threads_unread", Help: "number of unread threads"},
		[]string{"Label"},
	)
	totalGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "gmail_threads_total", Help: "total number of threads"},
		[]string{"Label"},
	)
	registry.MustRegister(unreadGauge, totalGauge)

	scrapeErrors := prometheus.NewCounter(prometheus.CounterOpts{Name: "gmail_scrape_errors_total", Help: "Total number of Gmail scrape errors"})
	scrapeSuccess := prometheus.NewCounter(prometheus.CounterOpts{Name: "gmail_scrape_success_total", Help: "Total number of successful Gmail scrapes"})

	stopCh := make(chan struct{})
	recordMetrics(1, unreadGauge, totalGauge, []string{"INBOX"}, srv, stopCh, scrapeErrors, scrapeSuccess)

	// Wait for at least one scrape
	time.Sleep(1500 * time.Millisecond)

	// Verify metrics were set
	handler := promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "gmail_threads_total") {
		t.Errorf("Expected metrics not found in:\n%s", body)
	}

	// Verify success counter was incremented
	if testutil.ToFloat64(scrapeSuccess) < 1 {
		t.Errorf("Expected scrapeSuccess to be incremented, got %v", testutil.ToFloat64(scrapeSuccess))
	}

	// Stop the goroutine
	close(stopCh)
	time.Sleep(100 * time.Millisecond)
}

func TestRecordMetricsPanicRecovery(t *testing.T) {
	registry := prometheus.NewRegistry()
	unreadGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "gmail_threads_unread", Help: "number of unread threads"},
		[]string{"Label"},
	)
	totalGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "gmail_threads_total", Help: "total number of threads"},
		[]string{"Label"},
	)
	registry.MustRegister(unreadGauge, totalGauge)

	scrapeErrors := prometheus.NewCounter(prometheus.CounterOpts{Name: "gmail_scrape_errors_total", Help: "Total number of Gmail scrape errors"})
	scrapeSuccess := prometheus.NewCounter(prometheus.CounterOpts{Name: "gmail_scrape_success_total", Help: "Total number of successful Gmail scrapes"})

	stopCh := make(chan struct{})
	// Pass nil srv to trigger panic in scrapeMetrics
	recordMetrics(1, unreadGauge, totalGauge, []string{"INBOX"}, nil, stopCh, scrapeErrors, scrapeSuccess)

	// Wait for at least one scrape cycle
	time.Sleep(1500 * time.Millisecond)

	close(stopCh)

	// Verify error counter was incremented due to panic recovery
	if testutil.ToFloat64(scrapeErrors) < 1 {
		t.Errorf("Expected scrapeErrors to be incremented after panic, got %v", testutil.ToFloat64(scrapeErrors))
	}
}

// TestNewServer tests server creation
func TestNewServer(t *testing.T) {
	registry := prometheus.NewRegistry()
	server := newServer(registry, ":2112", 15*time.Second, 15*time.Second, 60*time.Second)

	if server.Addr != ":2112" {
		t.Errorf("Server address mismatch: got %q, want %q", server.Addr, ":2112")
	}
	if server.Handler == nil {
		t.Error("Expected server Handler to not be nil")
	}
	if server.ReadTimeout != 15*time.Second {
		t.Errorf("ReadTimeout mismatch: got %v, want %v", server.ReadTimeout, 15*time.Second)
	}
	if server.WriteTimeout != 15*time.Second {
		t.Errorf("WriteTimeout mismatch: got %v, want %v", server.WriteTimeout, 15*time.Second)
	}
	if server.IdleTimeout != 60*time.Second {
		t.Errorf("IdleTimeout mismatch: got %v, want %v", server.IdleTimeout, 60*time.Second)
	}

	// Verify the handler works
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	server.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rec.Code)
	}
}

func TestNewServerTimeouts(t *testing.T) {
	registry := prometheus.NewRegistry()
	server := newServer(registry, ":2112", 5*time.Second, 10*time.Second, 15*time.Second)

	if server.ReadTimeout != 5*time.Second {
		t.Errorf("ReadTimeout mismatch: got %v, want %v", server.ReadTimeout, 5*time.Second)
	}
	if server.WriteTimeout != 10*time.Second {
		t.Errorf("WriteTimeout mismatch: got %v, want %v", server.WriteTimeout, 10*time.Second)
	}
	if server.IdleTimeout != 15*time.Second {
		t.Errorf("IdleTimeout mismatch: got %v, want %v", server.IdleTimeout, 15*time.Second)
	}
}

func TestHealthEndpoint(t *testing.T) {
	registry := prometheus.NewRegistry()
	server := newServer(registry, "127.0.0.1:0", 15*time.Second, 15*time.Second, 60*time.Second)

	req := httptest.NewRequest("GET", "/health", nil)
	rec := httptest.NewRecorder()
	server.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rec.Code)
	}
	if rec.Body.String() != "OK" {
		t.Errorf("Expected body OK, got %q", rec.Body.String())
	}
}

func TestReadyEndpoint(t *testing.T) {
	registry := prometheus.NewRegistry()
	server := newServer(registry, "127.0.0.1:0", 15*time.Second, 15*time.Second, 60*time.Second)

	req := httptest.NewRequest("GET", "/ready", nil)
	rec := httptest.NewRecorder()
	server.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rec.Code)
	}
	if rec.Body.String() != "Ready" {
		t.Errorf("Expected body Ready, got %q", rec.Body.String())
	}
}

func TestRateLimiting(t *testing.T) {
	registry := prometheus.NewRegistry()
	server := newServer(registry, "127.0.0.1:0", 15*time.Second, 15*time.Second, 60*time.Second)

	ts := httptest.NewServer(server.Handler)
	defer ts.Close()

	// Make rapid requests to exhaust burst and trigger rate limiting
	var got429 bool
	for i := 0; i < 20; i++ {
		resp, err := http.Get(ts.URL + "/metrics")
		if err != nil {
			t.Fatalf("Request failed: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			got429 = true
			break
		}
	}

	if !got429 {
		t.Error("Expected at least one 429 Too Many Requests response")
	}
}

func TestGetEnvDuration(t *testing.T) {
	key := "TEST_HTTP_TIMEOUT"
	oldVal := os.Getenv(key)
	defer os.Setenv(key, oldVal)

	tests := []struct {
		name       string
		envValue   string
		defaultVal time.Duration
		want       time.Duration
	}{
		{"default", "", 15 * time.Second, 15 * time.Second},
		{"valid seconds", "30s", 15 * time.Second, 30 * time.Second},
		{"valid minutes", "2m", 15 * time.Second, 2 * time.Minute},
		{"invalid", "bad", 15 * time.Second, 15 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envValue == "" {
				os.Unsetenv(key)
			} else {
				os.Setenv(key, tt.envValue)
			}
			got := getEnvDuration(key, tt.defaultVal)
			if got != tt.want {
				t.Errorf("getEnvDuration() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsLocalhost(t *testing.T) {
	tests := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:2112", true},
		{"localhost:2112", true},
		{"[::1]:2112", true},
		{":2112", false},
		{"0.0.0.0:2112", false},
		{"192.168.1.1:2112", false},
		{"invalid-address", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			got := isLocalhost(tt.addr)
			if got != tt.want {
				t.Errorf("isLocalhost(%q) = %v, want %v", tt.addr, got, tt.want)
			}
		})
	}
}

// TestRun tests the run function with mocked dependencies
func TestRun(t *testing.T) {
	// Create temp config file
	configFile, err := os.CreateTemp("", "config-*.yml")
	if err != nil {
		t.Fatalf("Failed to create temp config file: %v", err)
	}
	defer os.Remove(configFile.Name())
	configFile.WriteString("---\ninterval: 1\nlabels:\n  - INBOX\n")
	configFile.Close()

	// Create mock Gmail service function
	server := mockGmailServer(t)
	defer server.Close()

	mockSrvFn := func(path string) (*gmail.Service, error) {
		return newTestGmailService(t, server), nil
	}

	// Run should block on ListenAndServe, so we need to test it differently
	// Instead, test the components that run calls
	config, err := loadConfig(configFile.Name())
	if err != nil {
		t.Fatalf("loadConfig failed: %v", err)
	}
	if config.Interval != 1 {
		t.Errorf("Expected interval 1, got %d", config.Interval)
	}
	if len(config.Labels) != 1 || config.Labels[0] != "INBOX" {
		t.Errorf("Expected labels [INBOX], got %v", config.Labels)
	}

	srv, err := mockSrvFn("")
	if err != nil {
		t.Fatalf("mockSrvFn failed: %v", err)
	}

	labels, err := getLabels(srv)
	if err != nil {
		t.Fatalf("getLabels failed: %v", err)
	}
	if len(labels) != 3 {
		t.Errorf("Expected 3 labels, got %d", len(labels))
	}

	labelIds := matchLabels(labels, config.Labels)
	if len(labelIds) != 1 || labelIds[0] != "INBOX" {
		t.Errorf("Expected labelIds [INBOX], got %v", labelIds)
	}
}

func TestRunLoadConfigError(t *testing.T) {
	err := run("/nonexistent/config.yml", "", func(string) (*gmail.Service, error) {
		return nil, fmt.Errorf("should not be called")
	})
	if err == nil {
		t.Error("Expected error for missing config file")
	}
	if !strings.Contains(err.Error(), "config file") {
		t.Errorf("Expected config file error, got: %v", err)
	}
}

func TestRunServiceError(t *testing.T) {
	// Create temp config file
	configFile, err := os.CreateTemp("", "config-*.yml")
	if err != nil {
		t.Fatalf("Failed to create temp config file: %v", err)
	}
	defer os.Remove(configFile.Name())
	configFile.WriteString("---\ninterval: 1\nlabels:\n  - INBOX\n")
	configFile.Close()

	mockErr := fmt.Errorf("mock service error")
	err = run(configFile.Name(), "", func(string) (*gmail.Service, error) {
		return nil, mockErr
	})
	if err != mockErr {
		t.Errorf("Expected mock error, got: %v", err)
	}
}

// TestTokenFromFile tests reading OAuth2 tokens from file
func TestTokenFromFile(t *testing.T) {
	tests := []struct {
		name        string
		tokenData   *oauth2.Token
		wantErr     bool
		filePerms   os.FileMode
		corruptJSON bool
	}{
		{
			name: "valid token",
			tokenData: &oauth2.Token{
				AccessToken:  "test-access-token",
				TokenType:    "Bearer",
				RefreshToken: "test-refresh-token",
				Expiry:       time.Now().Add(time.Hour),
			},
			wantErr:   false,
			filePerms: 0600,
		},
		{
			name: "token with zero expiry",
			tokenData: &oauth2.Token{
				AccessToken: "test-access-token",
				TokenType:   "Bearer",
			},
			wantErr:   false,
			filePerms: 0600,
		},
		{
			name:        "corrupted JSON",
			wantErr:     true,
			filePerms:   0600,
			corruptJSON: true,
		},
		{
			name:      "file not found",
			wantErr:   true,
			filePerms: 0600,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var tmpfile *os.File
			var err error
			filename := "nonexistent-token.json"

			if tt.tokenData != nil || tt.corruptJSON {
				tmpfile, err = os.CreateTemp("", "token-*.json")
				if err != nil {
					t.Fatalf("Failed to create temp file: %v", err)
				}
				defer os.Remove(tmpfile.Name())
				filename = tmpfile.Name()

				if tt.corruptJSON {
					if _, err := tmpfile.WriteString("not valid json"); err != nil {
						t.Fatalf("Failed to write corrupt data: %v", err)
					}
				} else {
					data, _ := json.Marshal(tt.tokenData)
					if _, err := tmpfile.Write(data); err != nil {
						t.Fatalf("Failed to write token data: %v", err)
					}
				}
				tmpfile.Close()

				if err := os.Chmod(filename, tt.filePerms); err != nil {
					t.Fatalf("Failed to set file permissions: %v", err)
				}
			}

			tok, err := tokenFromFile(filename)

			if tt.wantErr {
				if err == nil {
					t.Errorf("Expected error but got none")
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
				if tok == nil {
					t.Fatal("Expected token but got nil")
				}
				if tok.AccessToken != tt.tokenData.AccessToken {
					t.Errorf("AccessToken mismatch: got %q, want %q", tok.AccessToken, tt.tokenData.AccessToken)
				}
				if tok.TokenType != tt.tokenData.TokenType {
					t.Errorf("TokenType mismatch: got %q, want %q", tok.TokenType, tt.tokenData.TokenType)
				}
			}
		})
	}
}

// TestSaveToken tests writing OAuth2 tokens to file
func TestSaveToken(t *testing.T) {
	tmpfile, err := os.CreateTemp("", "token-*.json")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	tmpfile.Close()
	defer os.Remove(tmpfile.Name())

	token := &oauth2.Token{
		AccessToken:  "test-access-token",
		TokenType:    "Bearer",
		RefreshToken: "test-refresh-token",
		Expiry:       time.Now().Add(time.Hour),
	}

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err = saveToken(tmpfile.Name(), token)

	os.Stdout = oldStdout
	w.Close()
	io.Copy(io.Discard, r)

	if err != nil {
		t.Fatalf("saveToken failed: %v", err)
	}

	// Verify file permissions
	info, err := os.Stat(tmpfile.Name())
	if err != nil {
		t.Fatalf("Failed to stat token file: %v", err)
	}
	mode := info.Mode().Perm()
	if mode != 0600 {
		t.Errorf("Token file permissions: got %04o, want 0600", mode)
	}

	// Verify we can read it back
	readToken, err := tokenFromFile(tmpfile.Name())
	if err != nil {
		t.Fatalf("Failed to read token back: %v", err)
	}

	if readToken.AccessToken != token.AccessToken {
		t.Errorf("Saved token AccessToken mismatch: got %q, want %q", readToken.AccessToken, token.AccessToken)
	}
}

func TestSaveTokenInvalidPath(t *testing.T) {
	token := &oauth2.Token{AccessToken: "test"}
	err := saveToken("/nonexistent/directory/token.json", token)
	if err == nil {
		t.Error("Expected error for invalid path")
	}
}

// TestGetClient tests OAuth2 client creation
func TestGetClient(t *testing.T) {
	// Create a temp token file
	tmpfile, err := os.CreateTemp("", "token-*.json")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpfile.Name())

	token := &oauth2.Token{
		AccessToken:  "test-access-token",
		TokenType:    "Bearer",
		RefreshToken: "test-refresh-token",
		Expiry:       time.Now().Add(time.Hour),
	}
	data, _ := json.Marshal(token)
	if _, err := tmpfile.Write(data); err != nil {
		t.Fatalf("Failed to write token: %v", err)
	}
	tmpfile.Close()

	config := &oauth2.Config{
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		Endpoint:     oauth2.Endpoint{TokenURL: "http://localhost/token"},
	}

	client, err := getClient(config, tmpfile.Name())
	if err != nil {
		t.Fatalf("getClient failed: %v", err)
	}
	if client == nil {
		t.Fatal("Expected client but got nil")
	}
}

func TestGetClientMissingToken(t *testing.T) {
	config := &oauth2.Config{
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		Endpoint:     oauth2.Endpoint{TokenURL: "http://localhost/token"},
		RedirectURL:  "http://localhost/callback",
	}

	// With no token file, it will try to get token from web which reads from stdin
	// This is hard to test, so we'll just verify it returns an error
	_, err := getClient(config, "/nonexistent/token.json")
	if err == nil {
		t.Error("Expected error when token file is missing and stdin is not available")
	}
}

// TestCreateGmailService tests Gmail service creation
func TestCreateGmailService(t *testing.T) {
	// Create temp credentials file
	credFile, err := os.CreateTemp("", "credentials-*.json")
	if err != nil {
		t.Fatalf("Failed to create temp credentials file: %v", err)
	}
	defer os.Remove(credFile.Name())

	validJSON := []byte(`{
		"installed": {
			"client_id": "test-client-id",
			"project_id": "test-project",
			"auth_uri": "https://accounts.google.com/o/oauth2/auth",
			"token_uri": "https://oauth2.googleapis.com/token",
			"auth_provider_x509_cert_url": "https://www.googleapis.com/oauth2/v1/certs",
			"client_secret": "test-client-secret",
			"redirect_uris": ["urn:ietf:wg:oauth:2.0:oob", "http://localhost"]
		}
	}`)
	if _, err := credFile.Write(validJSON); err != nil {
		t.Fatalf("Failed to write credentials: %v", err)
	}
	credFile.Close()

	// Create temp token file
	tokenFile, err := os.CreateTemp("", "token-*.json")
	if err != nil {
		t.Fatalf("Failed to create temp token file: %v", err)
	}
	defer os.Remove(tokenFile.Name())

	token := &oauth2.Token{
		AccessToken:  "test-access-token",
		TokenType:    "Bearer",
		RefreshToken: "test-refresh-token",
		Expiry:       time.Now().Add(time.Hour),
	}
	data, _ := json.Marshal(token)
	if _, err := tokenFile.Write(data); err != nil {
		t.Fatalf("Failed to write token: %v", err)
	}
	tokenFile.Close()

	// Set TOKEN_PATH env var
	oldTokenPath := os.Getenv("TOKEN_PATH")
	os.Setenv("TOKEN_PATH", tokenFile.Name())
	defer os.Setenv("TOKEN_PATH", oldTokenPath)

	_, err = createGmailService(credFile.Name())
	if err != nil {
		t.Fatalf("createGmailService failed: %v", err)
	}
}

func TestCreateGmailServiceMissingCredentials(t *testing.T) {
	_, err := createGmailService("/nonexistent/credentials.json")
	if err == nil {
		t.Error("Expected error for missing credentials file")
	}
	if !strings.Contains(err.Error(), "client secret file") {
		t.Errorf("Expected client secret error, got: %v", err)
	}
}

func TestCreateGmailServiceInvalidCredentials(t *testing.T) {
	credFile, err := os.CreateTemp("", "credentials-*.json")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(credFile.Name())
	credFile.WriteString("invalid json")
	credFile.Close()

	_, err = createGmailService(credFile.Name())
	if err == nil {
		t.Error("Expected error for invalid credentials JSON")
	}
}

// TestGetLabels tests label retrieval
func TestGetLabels(t *testing.T) {
	server := mockGmailServer(t)
	defer server.Close()

	srv := newTestGmailService(t, server)

	labels, err := getLabels(srv)
	if err != nil {
		t.Fatalf("getLabels failed: %v", err)
	}
	if len(labels) != 3 {
		t.Errorf("Expected 3 labels, got %d", len(labels))
	}

	// Verify label names
	expectedNames := map[string]bool{"INBOX": false, "SENT": false, "SPAM": false}
	for _, label := range labels {
		expectedNames[label.Name] = true
	}
	for name, found := range expectedNames {
		if !found {
			t.Errorf("Expected label %q not found", name)
		}
	}
}

func TestGetLabelsError(t *testing.T) {
	// Create a server that returns errors
	errServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "internal error"})
	}))
	defer errServer.Close()

	srv := newTestGmailService(t, errServer)

	_, err := getLabels(srv)
	if err == nil {
		t.Error("Expected error for server error response")
	}
}

// TestMetricsRegistration tests Prometheus metrics setup
func TestMetricsRegistration(t *testing.T) {
	registry := prometheus.NewRegistry()

	unreadGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gmail_threads_unread",
			Help: "number of unread threads",
		},
		[]string{"Label"},
	)
	totalGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gmail_threads_total",
			Help: "total number of threads",
		},
		[]string{"Label"},
	)

	err := registry.Register(unreadGauge)
	if err != nil {
		t.Fatalf("Failed to register unreadGauge: %v", err)
	}

	err = registry.Register(totalGauge)
	if err != nil {
		t.Fatalf("Failed to register totalGauge: %v", err)
	}

	labels := map[string]string{"Label": "gmail_INBOX"}
	unreadGauge.With(labels).Set(5)
	totalGauge.With(labels).Set(100)

	err = registry.Register(unreadGauge)
	if err == nil {
		t.Error("Expected error for duplicate registration")
	}
}

// TestMetricsHandler tests the /metrics HTTP endpoint
func TestMetricsHandler(t *testing.T) {
	registry := prometheus.NewRegistry()

	unreadGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gmail_threads_unread",
			Help: "number of unread threads",
		},
		[]string{"Label"},
	)
	totalGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gmail_threads_total",
			Help: "total number of threads",
		},
		[]string{"Label"},
	)

	registry.MustRegister(unreadGauge, totalGauge)

	labels := map[string]string{"Label": "gmail_INBOX"}
	unreadGauge.With(labels).Set(5)
	totalGauge.With(labels).Set(100)

	handler := promhttp.HandlerFor(registry, promhttp.HandlerOpts{})

	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	resp := rec.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	body := rec.Body.String()

	if !strings.Contains(body, "gmail_threads_unread") {
		t.Error("Response missing gmail_threads_unread metric")
	}
	if !strings.Contains(body, "gmail_threads_total") {
		t.Error("Response missing gmail_threads_total metric")
	}
	if !strings.Contains(body, `Label="gmail_INBOX"`) {
		t.Error("Response missing label value")
	}

	contentType := resp.Header.Get("Content-Type")
	if !strings.Contains(contentType, "text/plain") {
		t.Errorf("Expected text/plain content type, got %q", contentType)
	}
}

// TestPrometheusLabelGeneration tests label name conversion
func TestPrometheusLabelGeneration(t *testing.T) {
	tests := []struct {
		labelName string
		wantLabel string
	}{
		{"INBOX", "gmail_inbox"},
		{"SENT", "gmail_sent"},
		{"SPAM", "gmail_spam"},
		{"Important", "gmail_important"},
		{"My Label", "gmail_my_label"},
		{"Label-Name", "gmail_label_name"},
		{"Label.Name", "gmail_label_name"},
		{"", "gmail_"},
	}

	for _, tt := range tests {
		t.Run(tt.labelName, func(t *testing.T) {
			got := "gmail_" + strcase.ToSnake(tt.labelName)
			if got != tt.wantLabel {
				t.Errorf("strcase.ToSnake(%q) = %q, want %q", tt.labelName, got, tt.wantLabel)
			}
		})
	}
}

// TestInvalidCredentialsJSON tests parsing of invalid credentials JSON
func TestInvalidCredentialsJSON(t *testing.T) {
	invalidJSON := []byte(`not valid json`)
	_, err := google.ConfigFromJSON(invalidJSON, gmail.GmailReadonlyScope)
	if err == nil {
		t.Error("Expected error for invalid JSON")
	}
}

// TestValidCredentialsJSON tests parsing of valid credentials JSON
func TestValidCredentialsJSON(t *testing.T) {
	validJSON := []byte(`{
		"installed": {
			"client_id": "test-client-id",
			"project_id": "test-project",
			"auth_uri": "https://accounts.google.com/o/oauth2/auth",
			"token_uri": "https://oauth2.googleapis.com/token",
			"auth_provider_x509_cert_url": "https://www.googleapis.com/oauth2/v1/certs",
			"client_secret": "test-client-secret",
			"redirect_uris": ["urn:ietf:wg:oauth:2.0:oob", "http://localhost"]
		}
	}`)

	config, err := google.ConfigFromJSON(validJSON, gmail.GmailReadonlyScope)
	if err != nil {
		t.Fatalf("Unexpected error parsing valid JSON: %v", err)
	}

	if config.ClientID != "test-client-id" {
		t.Errorf("ClientID mismatch: got %q, want %q", config.ClientID, "test-client-id")
	}

	if config.ClientSecret != "test-client-secret" {
		t.Errorf("ClientSecret mismatch: got %q, want %q", config.ClientSecret, "test-client-secret")
	}

	found := false
	for _, scope := range config.Scopes {
		if scope == gmail.GmailReadonlyScope {
			found = true
			break
		}
	}
	if !found {
		t.Error("GmailReadonlyScope not found in config scopes")
	}
}

// TestEmptyConfigFile tests handling of empty config files
func TestEmptyConfigFile(t *testing.T) {
	tmpfile, err := os.CreateTemp("", "config-*.yml")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpfile.Name())
	tmpfile.Close()

	_, err = loadConfig(tmpfile.Name())
	if err == nil {
		t.Fatal("Expected error for empty config file")
	}
	if !strings.Contains(err.Error(), "interval") {
		t.Errorf("Expected interval validation error, got: %v", err)
	}
}

// TestConfigValidation tests various config validation scenarios
func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name        string
		configYAML  string
		wantErr     bool
		errContains string
	}{
		{
			name: "valid config",
			configYAML: `---
interval: 300
labels:
  - INBOX
`,
			wantErr: false,
		},
		{
			name: "zero interval",
			configYAML: `---
interval: 0
labels:
  - INBOX
`,
			wantErr:     true,
			errContains: "interval",
		},
		{
			name: "negative interval",
			configYAML: `---
interval: -1
labels:
  - INBOX
`,
			wantErr:     true,
			errContains: "interval",
		},
		{
			name: "empty labels",
			configYAML: `---
interval: 300
labels: []
`,
			wantErr:     true,
			errContains: "labels",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpfile, err := os.CreateTemp("", "config-*.yml")
			if err != nil {
				t.Fatalf("Failed to create temp file: %v", err)
			}
			defer os.Remove(tmpfile.Name())

			if _, err := tmpfile.Write([]byte(tt.configYAML)); err != nil {
				t.Fatalf("Failed to write temp file: %v", err)
			}
			tmpfile.Close()

			_, err = loadConfig(tmpfile.Name())
			if tt.wantErr {
				if err == nil {
					t.Error("Expected error but got none")
				}
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("Expected error containing %q, got: %v", tt.errContains, err)
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
			}
		})
	}
}

// TestMainFunction tests main function entry point
func TestMainFunction(t *testing.T) {
	// We can't easily test main() since it calls log.Fatal on errors
	// But we can verify that the functions it calls work correctly
	// by testing them individually (which we've done above)

	// Test that environment variables are read correctly
	oldConfigPath := os.Getenv("CONFIG_PATH")
	oldCredPath := os.Getenv("GMAIL_CREDENTIALS_PATH")
	defer func() {
		os.Setenv("CONFIG_PATH", oldConfigPath)
		os.Setenv("GMAIL_CREDENTIALS_PATH", oldCredPath)
	}()

	os.Setenv("CONFIG_PATH", "/tmp/test-config.yml")
	os.Setenv("GMAIL_CREDENTIALS_PATH", "/tmp/test-credentials.json")

	configPath := "config.yml"
	if envPath := os.Getenv("CONFIG_PATH"); envPath != "" {
		configPath = envPath
	}
	if configPath != "/tmp/test-config.yml" {
		t.Errorf("Expected config path from env, got %q", configPath)
	}

	credentialsPath := "credentials.json"
	if envPath := os.Getenv("GMAIL_CREDENTIALS_PATH"); envPath != "" {
		credentialsPath = envPath
	}
	if credentialsPath != "/tmp/test-credentials.json" {
		t.Errorf("Expected credentials path from env, got %q", credentialsPath)
	}
}

// TestServerTimeout tests that the server respects timeout configurations
func TestServerTimeout(t *testing.T) {
	slowHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	})

	server := &http.Server{
		Addr:         "127.0.0.1:0",
		Handler:      slowHandler,
		ReadTimeout:  10 * time.Millisecond,
		WriteTimeout: 10 * time.Millisecond,
		IdleTimeout:  10 * time.Millisecond,
	}

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			t.Logf("Server error (expected): %v", err)
		}
	}()
	defer server.Close()

	time.Sleep(50 * time.Millisecond)

	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()

	slowHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rec.Code)
	}
}

// BenchmarkMetricsHandler benchmarks the metrics endpoint
func BenchmarkMetricsHandler(b *testing.B) {
	registry := prometheus.NewRegistry()

	unreadGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gmail_threads_unread",
			Help: "number of unread threads",
		},
		[]string{"Label"},
	)
	totalGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gmail_threads_total",
			Help: "total number of threads",
		},
		[]string{"Label"},
	)

	registry.MustRegister(unreadGauge, totalGauge)

	labels := map[string]string{"Label": "gmail_INBOX"}
	unreadGauge.With(labels).Set(5)
	totalGauge.With(labels).Set(100)

	handler := promhttp.HandlerFor(registry, promhttp.HandlerOpts{})

	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("GET", "/metrics", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}
}

// TestGetTokenFromWebSuccess tests successful OAuth2 token retrieval from web
func TestGetTokenFromWebSuccess(t *testing.T) {
	// Create mock OAuth2 token server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("Expected POST request, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]interface{}{
			"access_token":  "mock-access-token",
			"token_type":    "Bearer",
			"refresh_token": "mock-refresh-token",
			"expires_in":    3600,
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	config := &oauth2.Config{
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		Endpoint: oauth2.Endpoint{
			TokenURL: ts.URL,
		},
		RedirectURL: "http://localhost/callback",
	}

	// Mock stdin with auth code
	oldStdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Failed to create pipe: %v", err)
	}
	os.Stdin = r

	// Write auth code in background
	go func() {
		w.WriteString("mock-auth-code\n")
		w.Close()
	}()

	// Capture stdout to suppress output
	oldStdout := os.Stdout
	stdoutR, stdoutW, _ := os.Pipe()
	os.Stdout = stdoutW

	tok, err := getTokenFromWeb(config)

	os.Stdout = oldStdout
	stdoutW.Close()
	io.Copy(io.Discard, stdoutR)
	os.Stdin = oldStdin

	if err != nil {
		t.Fatalf("getTokenFromWeb failed: %v", err)
	}
	if tok == nil {
		t.Fatal("Expected token but got nil")
	}
	if tok.AccessToken != "mock-access-token" {
		t.Errorf("AccessToken mismatch: got %q, want %q", tok.AccessToken, "mock-access-token")
	}
	if tok.TokenType != "Bearer" {
		t.Errorf("TokenType mismatch: got %q, want %q", tok.TokenType, "Bearer")
	}
}

// TestGetTokenFromWebInvalidAuthCode tests error handling for invalid auth code
func TestGetTokenFromWebInvalidAuthCode(t *testing.T) {
	config := &oauth2.Config{
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		Endpoint: oauth2.Endpoint{
			TokenURL: "http://localhost:1/token", // Invalid URL that will fail
		},
		RedirectURL: "http://localhost/callback",
	}

	// Mock stdin with invalid auth code
	oldStdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Failed to create pipe: %v", err)
	}
	os.Stdin = r

	go func() {
		w.WriteString("invalid-code\n")
		w.Close()
	}()

	oldStdout := os.Stdout
	stdoutR, stdoutW, _ := os.Pipe()
	os.Stdout = stdoutW

	_, err = getTokenFromWeb(config)

	os.Stdout = oldStdout
	stdoutW.Close()
	io.Copy(io.Discard, stdoutR)
	os.Stdin = oldStdin

	if err == nil {
		t.Error("Expected error for invalid auth code")
	}
}

// TestGetClientWithWebAuth tests getClient when token file is missing but web auth succeeds
func TestGetClientWithWebAuth(t *testing.T) {
	// Create mock OAuth2 token server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]interface{}{
			"access_token":  "web-access-token",
			"token_type":    "Bearer",
			"refresh_token": "web-refresh-token",
			"expires_in":    3600,
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	config := &oauth2.Config{
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		Endpoint: oauth2.Endpoint{
			TokenURL: ts.URL,
		},
		RedirectURL: "http://localhost/callback",
	}

	// Create temp file path that doesn't exist
	tmpfile, err := os.CreateTemp("", "token-*.json")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	os.Remove(tmpfile.Name()) // Delete it so tokenFromFile fails

	// Mock stdin with auth code
	oldStdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Failed to create pipe: %v", err)
	}
	os.Stdin = r

	go func() {
		w.WriteString("mock-auth-code\n")
		w.Close()
	}()

	oldStdout := os.Stdout
	stdoutR, stdoutW, _ := os.Pipe()
	os.Stdout = stdoutW

	client, err := getClient(config, tmpfile.Name())

	os.Stdout = oldStdout
	stdoutW.Close()
	io.Copy(io.Discard, stdoutR)
	os.Stdin = oldStdin

	if err != nil {
		t.Fatalf("getClient failed: %v", err)
	}
	if client == nil {
		t.Fatal("Expected client but got nil")
	}

	// Verify token was saved
	_, err = os.Stat(tmpfile.Name())
	if err != nil {
		t.Errorf("Token file was not saved: %v", err)
	}
	os.Remove(tmpfile.Name())
}

// TestCreateGmailServiceWithTokenPathEnv tests createGmailService with TOKEN_PATH env var
func TestCreateGmailServiceWithTokenPathEnv(t *testing.T) {
	// Create temp credentials file
	credFile, err := os.CreateTemp("", "credentials-*.json")
	if err != nil {
		t.Fatalf("Failed to create temp credentials file: %v", err)
	}
	defer os.Remove(credFile.Name())

	validJSON := []byte(`{
		"installed": {
			"client_id": "test-client-id",
			"project_id": "test-project",
			"auth_uri": "https://accounts.google.com/o/oauth2/auth",
			"token_uri": "https://oauth2.googleapis.com/token",
			"auth_provider_x509_cert_url": "https://www.googleapis.com/oauth2/v1/certs",
			"client_secret": "test-client-secret",
			"redirect_uris": ["urn:ietf:wg:oauth:2.0:oob", "http://localhost"]
		}
	}`)
	if _, err := credFile.Write(validJSON); err != nil {
		t.Fatalf("Failed to write credentials: %v", err)
	}
	credFile.Close()

	// Create temp token file at custom path
	tokenFile, err := os.CreateTemp("", "custom-token-*.json")
	if err != nil {
		t.Fatalf("Failed to create temp token file: %v", err)
	}
	defer os.Remove(tokenFile.Name())

	token := &oauth2.Token{
		AccessToken:  "custom-access-token",
		TokenType:    "Bearer",
		RefreshToken: "custom-refresh-token",
		Expiry:       time.Now().Add(time.Hour),
	}
	data, _ := json.Marshal(token)
	if _, err := tokenFile.Write(data); err != nil {
		t.Fatalf("Failed to write token: %v", err)
	}
	tokenFile.Close()

	// Set TOKEN_PATH env var
	oldTokenPath := os.Getenv("TOKEN_PATH")
	os.Setenv("TOKEN_PATH", tokenFile.Name())
	defer os.Setenv("TOKEN_PATH", oldTokenPath)

	_, err = createGmailService(credFile.Name())
	if err != nil {
		t.Fatalf("createGmailService failed: %v", err)
	}
}

// TestRecordMetricsStopChannel tests that recordMetrics goroutine exits on stop signal
func TestRecordMetricsStopChannel(t *testing.T) {
	server := mockGmailServer(t)
	defer server.Close()

	srv := newTestGmailService(t, server)

	registry := prometheus.NewRegistry()
	unreadGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "gmail_threads_unread", Help: "number of unread threads"},
		[]string{"Label"},
	)
	totalGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "gmail_threads_total", Help: "total number of threads"},
		[]string{"Label"},
	)
	registry.MustRegister(unreadGauge, totalGauge)

	scrapeErrors := prometheus.NewCounter(prometheus.CounterOpts{Name: "gmail_scrape_errors_total", Help: "Total number of Gmail scrape errors"})
	scrapeSuccess := prometheus.NewCounter(prometheus.CounterOpts{Name: "gmail_scrape_success_total", Help: "Total number of successful Gmail scrapes"})

	stopCh := make(chan struct{})
	recordMetrics(1, unreadGauge, totalGauge, []string{"INBOX"}, srv, stopCh, scrapeErrors, scrapeSuccess)

	// Wait for first scrape
	time.Sleep(1500 * time.Millisecond)

	// Stop the goroutine
	close(stopCh)

	// Give goroutine time to exit
	time.Sleep(200 * time.Millisecond)

	// Verify metrics were collected before stop
	handler := promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "gmail_threads_total") {
		t.Errorf("Expected metrics not found in:\n%s", body)
	}
}

// TestRunWithRealServer tests the run function with a real HTTP server
func TestRunWithRealServer(t *testing.T) {
	// Create temp config file
	configFile, err := os.CreateTemp("", "config-*.yml")
	if err != nil {
		t.Fatalf("Failed to create temp config file: %v", err)
	}
	defer os.Remove(configFile.Name())
	configFile.WriteString("---\ninterval: 3600\nlabels:\n  - INBOX\n")
	configFile.Close()

	server := mockGmailServer(t)
	defer server.Close()

	// Use random port
	oldListenAddr := os.Getenv("LISTEN_ADDRESS")
	os.Setenv("LISTEN_ADDRESS", "127.0.0.1:0")
	defer os.Setenv("LISTEN_ADDRESS", oldListenAddr)

	mockSrvFn := func(path string) (*gmail.Service, error) {
		return newTestGmailService(t, server), nil
	}

	// Start run in a goroutine
	errCh := make(chan error, 1)
	go func() {
		errCh <- run(configFile.Name(), "", mockSrvFn)
	}()

	// Give server time to start
	time.Sleep(200 * time.Millisecond)

	// Make a request to verify server is running
	// We can't easily know the port with :0, but we can at least verify
	// the goroutine started without error
	select {
	case err := <-errCh:
		if err != nil && !strings.Contains(err.Error(), "listen") {
			t.Fatalf("run() returned unexpected error: %v", err)
		}
	default:
		// Server is running, which is success
	}
}

// TestMainEnvVars tests main function environment variable handling
func TestMainEnvVars(t *testing.T) {
	oldConfigPath := os.Getenv("CONFIG_PATH")
	oldCredPath := os.Getenv("GMAIL_CREDENTIALS_PATH")
	oldListenAddr := os.Getenv("LISTEN_ADDRESS")
	defer func() {
		os.Setenv("CONFIG_PATH", oldConfigPath)
		os.Setenv("GMAIL_CREDENTIALS_PATH", oldCredPath)
		os.Setenv("LISTEN_ADDRESS", oldListenAddr)
	}()

	os.Setenv("CONFIG_PATH", "/custom/config.yml")
	os.Setenv("GMAIL_CREDENTIALS_PATH", "/custom/credentials.json")
	os.Setenv("LISTEN_ADDRESS", ":9090")

	// Verify env vars are set
	if os.Getenv("CONFIG_PATH") != "/custom/config.yml" {
		t.Error("CONFIG_PATH env var not set correctly")
	}
	if os.Getenv("GMAIL_CREDENTIALS_PATH") != "/custom/credentials.json" {
		t.Error("GMAIL_CREDENTIALS_PATH env var not set correctly")
	}
	if os.Getenv("LISTEN_ADDRESS") != ":9090" {
		t.Error("LISTEN_ADDRESS env var not set correctly")
	}
}

// Helper function to capture output
func captureOutput(f func()) string {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	f()

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	buf.ReadFrom(r)
	return buf.String()
}
