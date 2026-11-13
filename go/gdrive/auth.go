package gdrive

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// DefaultRelayBaseURL is the shared Cloudflare OAuth relay operated by vault-share.
const DefaultRelayBaseURL = "https://vault-share-auth.bob-hyman.workers.dev"

const (
	proactiveRefreshWindow = 60 * time.Second
	authFailedCooldown     = 60 * time.Second
)

// AuthError indicates authentication is missing, expired, or rejected by the relay.
type AuthError struct {
	StatusCode int
	Message    string
}

func (e *AuthError) Error() string {
	return e.Message
}

type tokenRefreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

type tokenRefreshResponse struct {
	AccessToken  string `json:"access_token"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
}

// TokenManager caches Google OAuth access tokens and mints new ones via the relay's
// refresh endpoint using an un-throttled HTTP client (outside the Drive semaphore).
type TokenManager struct {
	httpClient   *http.Client
	relayBaseURL string
	now          func() time.Time

	mu             sync.Mutex
	refreshToken   string
	pendingRotated string
	accessToken    string
	expiresAt      time.Time
	authFailedAt   time.Time
}

// NewTokenManager creates a TokenManager backed by the provided un-throttled HTTP client.
func NewTokenManager(httpClient *http.Client, relayBaseURL string) *TokenManager {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if strings.TrimSpace(relayBaseURL) == "" {
		relayBaseURL = DefaultRelayBaseURL
	}
	return &TokenManager{
		httpClient:   httpClient,
		relayBaseURL: strings.TrimRight(relayBaseURL, "/"),
		now:          time.Now,
	}
}

// SetRefreshToken installs or replaces the active OAuth refresh token and clears cached access tokens.
func (tm *TokenManager) SetRefreshToken(refreshToken string) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.refreshToken = strings.TrimSpace(refreshToken)
	tm.pendingRotated = ""
	tm.accessToken = ""
	tm.expiresAt = time.Time{}
	tm.authFailedAt = time.Time{}
}

// Invalidate drops the cached access token so the next AccessToken call forces a refresh.
func (tm *TokenManager) Invalidate() {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.accessToken = ""
	tm.expiresAt = time.Time{}
}

// TakeRotatedRefreshToken returns and clears any rotated refresh token received during refresh.
func (tm *TokenManager) TakeRotatedRefreshToken() string {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tok := tm.pendingRotated
	tm.pendingRotated = ""
	return tok
}

// AccessToken returns a valid Bearer access token, refreshing proactively via the relay when needed.
// Concurrent callers serialize on tm.mu so only one refresh request executes.
func (tm *TokenManager) AccessToken(ctx context.Context) (string, error) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if tm.refreshToken == "" {
		return "", &AuthError{Message: "not authenticated: log in to Google Drive in settings"}
	}

	now := tm.now()
	if !tm.authFailedAt.IsZero() && now.Sub(tm.authFailedAt) < authFailedCooldown {
		return "", &AuthError{Message: "authentication failed recently: log in again in settings"}
	}

	if tm.accessToken != "" && now.Before(tm.expiresAt.Add(-proactiveRefreshWindow)) {
		return tm.accessToken, nil
	}

	return tm.performRefreshLocked(ctx)
}

func (tm *TokenManager) performRefreshLocked(ctx context.Context) (string, error) {
	payload, err := json.Marshal(tokenRefreshRequest{RefreshToken: tm.refreshToken})
	if err != nil {
		return "", fmt.Errorf("encode refresh request: %w", err)
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		tm.relayBaseURL+"/google/token/refresh",
		bytes.NewReader(payload),
	)
	if err != nil {
		return "", fmt.Errorf("build refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := tm.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("token refresh network error: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read token refresh response: %w", err)
	}

	if resp.StatusCode >= 400 {
		if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized {
			tm.authFailedAt = tm.now()
			return "", &AuthError{
				StatusCode: resp.StatusCode,
				Message:    fmt.Sprintf("token refresh rejected (%d)", resp.StatusCode),
			}
		}
		return "", fmt.Errorf("token refresh failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed tokenRefreshResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("parse token refresh response: %w", err)
	}
	if parsed.AccessToken == "" || parsed.ExpiresIn <= 0 {
		return "", &AuthError{Message: "unexpected token refresh response shape"}
	}

	tm.accessToken = parsed.AccessToken
	tm.expiresAt = tm.now().Add(time.Duration(parsed.ExpiresIn) * time.Second)
	tm.authFailedAt = time.Time{}

	if parsed.RefreshToken != "" && parsed.RefreshToken != tm.refreshToken {
		tm.refreshToken = parsed.RefreshToken
		tm.pendingRotated = parsed.RefreshToken
	}

	return tm.accessToken, nil
}
