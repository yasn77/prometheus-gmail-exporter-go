package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
)

func getTokenFromEnvironment() (*oauth2.Token, error) {
	if tokenJSON := os.Getenv("GMAIL_OAUTH_TOKEN"); tokenJSON != "" {
		tok := &oauth2.Token{}
		if err := json.Unmarshal([]byte(tokenJSON), tok); err != nil {
			return nil, fmt.Errorf("invalid GMAIL_OAUTH_TOKEN: %w", err)
		}
		return tok, nil
	}

	if secretPath := os.Getenv("TOKEN_SECRET_PATH"); secretPath != "" {
		return tokenFromFile(secretPath)
	}

	return nil, fmt.Errorf("no OAuth2 token found in environment: set GMAIL_OAUTH_TOKEN or TOKEN_SECRET_PATH")
}

func getClient(config *oauth2.Config, tokenPath string) (*http.Client, error) {
	tok, err := getTokenFromEnvironment()
	if err != nil {
		tok, err = tokenFromFile(tokenPath)
		if err != nil {
			tok, err = getTokenFromWeb(config)
			if err != nil {
				return nil, fmt.Errorf("failed to get token from web: %w", err)
			}
			if err := saveToken(tokenPath, tok); err != nil {
				return nil, fmt.Errorf("failed to save token: %w", err)
			}
		} else {
			log.Printf("Warning: using file-based token storage at %s; consider using GMAIL_OAUTH_TOKEN or TOKEN_SECRET_PATH environment variables for improved security", tokenPath)
		}
	}
	return config.Client(context.Background(), tok), nil
}

func generateStateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate random state: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func getTokenFromWeb(config *oauth2.Config) (*oauth2.Token, error) {
	state, err := generateStateToken()
	if err != nil {
		return nil, err
	}

	authURL := config.AuthCodeURL(state, oauth2.AccessTypeOffline)
	fmt.Printf("Go to the following link in your browser then type the "+
		"authorization code: \n%v\n", authURL)

	var authCode string
	if _, err := fmt.Scan(&authCode); err != nil {
		return nil, fmt.Errorf("unable to read authorization code: %w", err)
	}

	tok, err := config.Exchange(context.TODO(), authCode)
	if err != nil {
		return nil, fmt.Errorf("unable to retrieve token from web: %w", err)
	}
	return tok, nil
}

func tokenFromFile(file string) (*oauth2.Token, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	tok := &oauth2.Token{}
	err = json.NewDecoder(f).Decode(tok)
	return tok, err
}

func saveToken(path string, token *oauth2.Token) error {
	fmt.Printf("Saving credential file to: %s\n", path)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("unable to cache oauth token: %w", err)
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(token)
}

// TokenManager handles OAuth2 token lifecycle with thread-safe access
// and background refresh.
type TokenManager struct {
	config    *oauth2.Config
	token     *oauth2.Token
	tokenPath string
	mu        sync.RWMutex
	stopCh    chan struct{}
	wg        sync.WaitGroup
}

// NewTokenManager creates a new TokenManager.
func NewTokenManager(config *oauth2.Config, token *oauth2.Token, tokenPath string) *TokenManager {
	return &TokenManager{
		config:    config,
		token:     token,
		tokenPath: tokenPath,
		stopCh:    make(chan struct{}),
	}
}

// GetToken returns the current token in a thread-safe manner.
func (tm *TokenManager) GetToken() *oauth2.Token {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	return tm.token
}

// Refresh refreshes the OAuth2 token using the configured token source
// and persists it to disk if a tokenPath is configured.
func (tm *TokenManager) Refresh() error {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	ctx := context.Background()
	ts := tm.config.TokenSource(ctx, tm.token)
	newToken, err := ts.Token()
	if err != nil {
		return fmt.Errorf("failed to refresh token: %w", err)
	}

	tm.token = newToken

	if tm.tokenPath != "" {
		if err := saveToken(tm.tokenPath, newToken); err != nil {
			return fmt.Errorf("failed to save refreshed token: %w", err)
		}
	}

	return nil
}

// Start begins a background goroutine that checks token expiry every
// minute and proactively refreshes it when within 5 minutes of expiry.
func (tm *TokenManager) Start() {
	tm.wg.Add(1)
	go func() {
		defer tm.wg.Done()
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				tm.mu.RLock()
				needsRefresh := !tm.token.Expiry.IsZero() && tm.token.Expiry.Before(time.Now().Add(5*time.Minute))
				tm.mu.RUnlock()

				if needsRefresh {
					if err := tm.Refresh(); err != nil {
						log.Printf("Token refresh failed: %v", err)
					}
				}
			case <-tm.stopCh:
				return
			}
		}
	}()
}

// Stop signals the background goroutine to exit and waits for it to finish.
func (tm *TokenManager) Stop() {
	close(tm.stopCh)
	tm.wg.Wait()
}

func createGmailService(credentialsPath string) (*gmail.Service, error) {
	ctx := context.Background()
	b, err := os.ReadFile(credentialsPath)
	if err != nil {
		return nil, fmt.Errorf("unable to read client secret file: %w", err)
	}

	config, err := google.ConfigFromJSON(b, gmail.GmailReadonlyScope)
	if err != nil {
		return nil, fmt.Errorf("unable to parse client secret file to config: %w", err)
	}

	tokenPath := "token.json"
	if envPath := os.Getenv("TOKEN_PATH"); envPath != "" {
		tokenPath = envPath
	}

	var tok *oauth2.Token

	tok, err = getTokenFromEnvironment()
	if err != nil {
		tok, err = tokenFromFile(tokenPath)
		if err != nil {
			log.Printf("Warning: using file-based token storage; consider using GMAIL_OAUTH_TOKEN or TOKEN_SECRET_PATH environment variables for improved security")
			tok, err = getTokenFromWeb(config)
			if err != nil {
				return nil, fmt.Errorf("failed to get token from web: %w", err)
			}
			if err := saveToken(tokenPath, tok); err != nil {
				return nil, fmt.Errorf("failed to save token: %w", err)
			}
		}
	}

	tm := NewTokenManager(config, tok, tokenPath)
	tm.Start()

	client := config.Client(ctx, tm.GetToken())

	srv, err := gmail.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return nil, fmt.Errorf("unable to retrieve Gmail client: %w", err)
	}

	return srv, nil
}

func getLabels(srv *gmail.Service) ([]*gmail.Label, error) {
	user := "me"
	r, err := srv.Users.Labels.List(user).Do()
	if err != nil {
		return nil, fmt.Errorf("unable to retrieve labels: %w", err)
	}
	return r.Labels, nil
}
