package gdrive

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTokenManagerRefreshRotationAndProactiveReuse(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/google/token/refresh" || r.Method != http.MethodPost {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		var req tokenRefreshRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		n := calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		resp := tokenRefreshResponse{
			AccessToken: "access-" + string(rune('0'+n)),
			ExpiresIn:   3600,
		}
		if n == 1 {
			resp.RefreshToken = "rotated-rt-2"
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	tm := NewTokenManager(srv.Client(), srv.URL)
	tm.now = func() time.Time { return now }
	tm.SetRefreshToken("initial-rt-1")

	// Concurrent first access should deduplicate into a single refresh call.
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tok, err := tm.AccessToken(context.Background())
			if err != nil {
				t.Errorf("AccessToken error: %v", err)
			}
			if tok != "access-1" {
				t.Errorf("AccessToken = %q, want access-1", tok)
			}
		}()
	}
	wg.Wait()

	if calls.Load() != 1 {
		t.Fatalf("refresh calls = %d, want 1", calls.Load())
	}
	if rot := tm.TakeRotatedRefreshToken(); rot != "rotated-rt-2" {
		t.Fatalf("TakeRotatedRefreshToken() = %q, want rotated-rt-2", rot)
	}
	if rot := tm.TakeRotatedRefreshToken(); rot != "" {
		t.Fatalf("second TakeRotatedRefreshToken() = %q, want empty", rot)
	}

	// Still outside proactive refresh window: reuses cached token.
	now = now.Add(58 * time.Minute)
	tok, err := tm.AccessToken(context.Background())
	if err != nil || tok != "access-1" || calls.Load() != 1 {
		t.Fatalf("expected cached token, got tok=%q err=%v calls=%d", tok, err, calls.Load())
	}

	// Inside proactive 60s window: refreshes automatically.
	now = now.Add(90 * time.Second)
	tok, err = tm.AccessToken(context.Background())
	if err != nil || tok != "access-2" || calls.Load() != 2 {
		t.Fatalf("expected proactive refresh, got tok=%q err=%v calls=%d", tok, err, calls.Load())
	}
}

func TestTokenManagerCooldownOnAuthRejection(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, "invalid_grant", http.StatusBadRequest)
	}))
	defer srv.Close()

	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	tm := NewTokenManager(srv.Client(), srv.URL)
	tm.now = func() time.Time { return now }
	tm.SetRefreshToken("bad-rt")

	_, err := tm.AccessToken(context.Background())
	var authErr *AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("expected AuthError, got %v", err)
	}
	// Second immediate call hits cooldown without hitting network again.
	_, err = tm.AccessToken(context.Background())
	if !errors.As(err, &authErr) {
		t.Fatalf("expected cooldown AuthError, got %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected 1 network call during cooldown, got %d", calls.Load())
	}
}
