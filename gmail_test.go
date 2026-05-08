package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func TestGetTokenFromEnvironmentWithEnvVar(t *testing.T) {
	token := &oauth2.Token{
		AccessToken:  "env-access-token",
		TokenType:    "Bearer",
		RefreshToken: "env-refresh-token",
		Expiry:       time.Now().Add(time.Hour),
	}
	data, _ := json.Marshal(token)

	oldEnv := os.Getenv("GMAIL_OAUTH_TOKEN")
	os.Setenv("GMAIL_OAUTH_TOKEN", string(data))
	defer os.Setenv("GMAIL_OAUTH_TOKEN", oldEnv)

	// Ensure TOKEN_SECRET_PATH is not set
	oldSecretPath := os.Getenv("TOKEN_SECRET_PATH")
	os.Unsetenv("TOKEN_SECRET_PATH")
	defer os.Setenv("TOKEN_SECRET_PATH", oldSecretPath)

	tok, err := getTokenFromEnvironment()
	if err != nil {
		t.Fatalf("getTokenFromEnvironment failed: %v", err)
	}
	if tok.AccessToken != "env-access-token" {
		t.Errorf("AccessToken mismatch: got %q, want %q", tok.AccessToken, "env-access-token")
	}
	if tok.TokenType != "Bearer" {
		t.Errorf("TokenType mismatch: got %q, want %q", tok.TokenType, "Bearer")
	}
}

func TestGetTokenFromEnvironmentWithSecretPath(t *testing.T) {
	token := &oauth2.Token{
		AccessToken:  "file-access-token",
		TokenType:    "Bearer",
		RefreshToken: "file-refresh-token",
		Expiry:       time.Now().Add(time.Hour),
	}
	data, _ := json.Marshal(token)

	tmpfile, err := os.CreateTemp("", "token-*.json")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpfile.Name())

	if _, err := tmpfile.Write(data); err != nil {
		t.Fatalf("Failed to write token: %v", err)
	}
	tmpfile.Close()

	oldEnv := os.Getenv("GMAIL_OAUTH_TOKEN")
	os.Unsetenv("GMAIL_OAUTH_TOKEN")
	defer os.Setenv("GMAIL_OAUTH_TOKEN", oldEnv)

	oldSecretPath := os.Getenv("TOKEN_SECRET_PATH")
	os.Setenv("TOKEN_SECRET_PATH", tmpfile.Name())
	defer os.Setenv("TOKEN_SECRET_PATH", oldSecretPath)

	tok, err := getTokenFromEnvironment()
	if err != nil {
		t.Fatalf("getTokenFromEnvironment failed: %v", err)
	}
	if tok.AccessToken != "file-access-token" {
		t.Errorf("AccessToken mismatch: got %q, want %q", tok.AccessToken, "file-access-token")
	}
}

func TestGetTokenFromEnvironmentWithNeither(t *testing.T) {
	oldEnv := os.Getenv("GMAIL_OAUTH_TOKEN")
	os.Unsetenv("GMAIL_OAUTH_TOKEN")
	defer os.Setenv("GMAIL_OAUTH_TOKEN", oldEnv)

	oldSecretPath := os.Getenv("TOKEN_SECRET_PATH")
	os.Unsetenv("TOKEN_SECRET_PATH")
	defer os.Setenv("TOKEN_SECRET_PATH", oldSecretPath)

	_, err := getTokenFromEnvironment()
	if err == nil {
		t.Error("Expected error when neither env var is set")
	}
	if !strings.Contains(err.Error(), "GMAIL_OAUTH_TOKEN") {
		t.Errorf("Expected error mentioning env vars, got: %v", err)
	}
}

func TestTokenManagerCreation(t *testing.T) {
	config := &oauth2.Config{
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
	}
	token := &oauth2.Token{
		AccessToken: "test-token",
		TokenType:   "Bearer",
	}

	tm := NewTokenManager(config, token, "/tmp/token.json")
	if tm == nil {
		t.Fatal("Expected TokenManager but got nil")
	}

	tok := tm.GetToken()
	if tok.AccessToken != "test-token" {
		t.Errorf("AccessToken mismatch: got %q, want %q", tok.AccessToken, "test-token")
	}
}

func TestTokenManagerGetTokenThreadSafe(t *testing.T) {
	config := &oauth2.Config{}
	token := &oauth2.Token{AccessToken: "initial"}

	tm := NewTokenManager(config, token, "")

	// Concurrent reads should not race
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			_ = tm.GetToken()
		}
		close(done)
	}()

	for i := 0; i < 100; i++ {
		_ = tm.GetToken()
	}

	<-done
}

func TestTokenManagerRefresh(t *testing.T) {
	// Create mock OAuth2 token server
	ts := oauth2MockServer(t)
	defer ts.Close()

	config := &oauth2.Config{
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		Endpoint: oauth2.Endpoint{
			TokenURL: ts.URL,
		},
	}

	initialToken := &oauth2.Token{
		AccessToken:  "old-access-token",
		RefreshToken: "refresh-token",
		Expiry:       time.Now().Add(-time.Hour), // expired
	}

	tm := NewTokenManager(config, initialToken, "")
	err := tm.Refresh()
	if err != nil {
		t.Fatalf("Refresh failed: %v", err)
	}

	tok := tm.GetToken()
	if tok.AccessToken != "new-access-token" {
		t.Errorf("AccessToken mismatch: got %q, want %q", tok.AccessToken, "new-access-token")
	}
	if tok.TokenType != "Bearer" {
		t.Errorf("TokenType mismatch: got %q, want %q", tok.TokenType, "Bearer")
	}
}

func TestTokenManagerRefreshAndSave(t *testing.T) {
	ts := oauth2MockServer(t)
	defer ts.Close()

	config := &oauth2.Config{
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		Endpoint: oauth2.Endpoint{
			TokenURL: ts.URL,
		},
	}

	tmpfile, err := os.CreateTemp("", "token-*.json")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	tmpfile.Close()
	defer os.Remove(tmpfile.Name())

	initialToken := &oauth2.Token{
		AccessToken:  "old-access-token",
		RefreshToken: "refresh-token",
		Expiry:       time.Now().Add(-time.Hour),
	}

	tm := NewTokenManager(config, initialToken, tmpfile.Name())
	err = tm.Refresh()
	if err != nil {
		t.Fatalf("Refresh failed: %v", err)
	}

	// Verify file was saved
	savedToken, err := tokenFromFile(tmpfile.Name())
	if err != nil {
		t.Fatalf("Failed to read saved token: %v", err)
	}
	if savedToken.AccessToken != "new-access-token" {
		t.Errorf("Saved token AccessToken mismatch: got %q, want %q", savedToken.AccessToken, "new-access-token")
	}
}

func TestTokenManagerRefreshWithoutTokenPath(t *testing.T) {
	ts := oauth2MockServer(t)
	defer ts.Close()

	config := &oauth2.Config{
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		Endpoint: oauth2.Endpoint{
			TokenURL: ts.URL,
		},
	}

	initialToken := &oauth2.Token{
		AccessToken:  "old-access-token",
		RefreshToken: "refresh-token",
		Expiry:       time.Now().Add(-time.Hour),
	}

	tm := NewTokenManager(config, initialToken, "")
	err := tm.Refresh()
	if err != nil {
		t.Fatalf("Refresh failed: %v", err)
	}

	tok := tm.GetToken()
	if tok.AccessToken != "new-access-token" {
		t.Errorf("AccessToken mismatch: got %q, want %q", tok.AccessToken, "new-access-token")
	}
}

func TestTokenManagerStartStop(t *testing.T) {
	ts := oauth2MockServer(t)
	defer ts.Close()

	config := &oauth2.Config{
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		Endpoint: oauth2.Endpoint{
			TokenURL: ts.URL,
		},
	}

	initialToken := &oauth2.Token{
		AccessToken:  "old-token",
		RefreshToken: "refresh-token",
		Expiry:       time.Now().Add(-time.Hour), // expired
	}

	tm := NewTokenManager(config, initialToken, "")
	tm.Start()

	// Allow goroutine to start
	time.Sleep(50 * time.Millisecond)

	tm.Stop()

	// Stop should complete without hanging
}

func TestGenerateStateToken(t *testing.T) {
	state1, err := generateStateToken()
	if err != nil {
		t.Fatalf("generateStateToken failed: %v", err)
	}
	if len(state1) != 64 {
		t.Errorf("Expected state length 64, got %d", len(state1))
	}

	state2, err := generateStateToken()
	if err != nil {
		t.Fatalf("generateStateToken failed: %v", err)
	}
	if state1 == state2 {
		t.Error("Expected two different state tokens")
	}
}

func TestGetTokenFromWebRandomState(t *testing.T) {
	ts := oauth2MockServer(t)
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

	go func() {
		w.WriteString("mock-auth-code\n")
		w.Close()
	}()

	// Capture stdout to inspect the auth URL
	oldStdout := os.Stdout
	stdoutR, stdoutW, _ := os.Pipe()
	os.Stdout = stdoutW

	_, err = getTokenFromWeb(config)

	os.Stdout = oldStdout
	stdoutW.Close()
	os.Stdin = oldStdin

	if err != nil {
		t.Fatalf("getTokenFromWeb failed: %v", err)
	}

	var buf bytes.Buffer
	io.Copy(&buf, stdoutR)
	output := buf.String()

	if !strings.Contains(output, "state=") {
		t.Errorf("Expected auth URL to contain state parameter, got:\n%s", output)
	}
}

func TestSaveTokenSuccess(t *testing.T) {
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

	info, err := os.Stat(tmpfile.Name())
	if err != nil {
		t.Fatalf("Failed to stat token file: %v", err)
	}
	mode := info.Mode().Perm()
	if mode != 0600 {
		t.Errorf("Token file permissions: got %04o, want 0600", mode)
	}

	readToken, err := tokenFromFile(tmpfile.Name())
	if err != nil {
		t.Fatalf("Failed to read token back: %v", err)
	}
	if readToken.AccessToken != token.AccessToken {
		t.Errorf("Saved token AccessToken mismatch: got %q, want %q", readToken.AccessToken, token.AccessToken)
	}
}

func TestSaveTokenError(t *testing.T) {
	token := &oauth2.Token{AccessToken: "test"}
	err := saveToken("/nonexistent/directory/token.json", token)
	if err == nil {
		t.Error("Expected error for invalid path")
	}
}

// oauth2MockServer creates a test server that mimics an OAuth2 token endpoint.
func oauth2MockServer(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("Expected POST request, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]interface{}{
			"access_token":  "new-access-token",
			"token_type":    "Bearer",
			"refresh_token": "new-refresh-token",
			"expires_in":    3600,
		}
		json.NewEncoder(w).Encode(resp)
	}))
}
