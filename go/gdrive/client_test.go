package gdrive

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func noSleepRetryOptions() RetryOptions {
	return RetryOptions{
		MaxAttempts: 4,
		BaseDelay:   1 * time.Millisecond,
		MaxDelay:    5 * time.Millisecond,
		Sleep:       func(_ context.Context, _ time.Duration) error { return nil },
		Jitter:      func(_ time.Duration) time.Duration { return 0 },
	}
}

func TestClientRetryBackoffAndNonRetryable403(t *testing.T) {
	var authCalls atomic.Int32
	var driveCalls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/google/token/refresh" {
			authCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(tokenRefreshResponse{AccessToken: "tok-1", ExpiresIn: 3600})
			return
		}
		attempt := driveCalls.Add(1)
		switch attempt {
		case 1:
			w.Header().Set("Retry-After", "0")
			http.Error(w, "too many", http.StatusTooManyRequests)
		case 2:
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"errors":[{"reason":"userRateLimitExceeded"}]}}`))
		case 3:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"files":[{"id":"f1","name":"alpha.md","mimeType":"text/markdown"},{"id":"f2","name":"image.png","mimeType":"image/png"}]}`))
		}
	}))
	defer srv.Close()

	tm := NewTokenManager(srv.Client(), srv.URL)
	tm.SetRefreshToken("rt-1")
	client := NewClient(srv.Client(), tm, noSleepRetryOptions())
	client.SetEndpoints(srv.URL+"/drive/v3", srv.URL+"/upload/drive/v3")

	files, err := client.ListFiles(context.Background(), "folder-1")
	if err != nil {
		t.Fatalf("ListFiles failed: %v", err)
	}
	if driveCalls.Load() != 3 {
		t.Fatalf("driveCalls = %d, want 3", driveCalls.Load())
	}
	if len(files) != 1 || files[0].Name != "alpha.md" {
		t.Fatalf("unexpected files: %+v", files)
	}
}

func TestClient401RefreshRetryWithoutSemaphoreDeadlock(t *testing.T) {
	var tokenSeq atomic.Int32
	var driveCalls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/google/token/refresh" {
			n := tokenSeq.Add(1)
			tok := "stale-tok"
			if n >= 2 {
				tok = "fresh-tok"
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(tokenRefreshResponse{AccessToken: tok, ExpiresIn: 3600})
			return
		}
		driveCalls.Add(1)
		if r.Header.Get("Authorization") != "Bearer fresh-tok" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte("note body"))
	}))
	defer srv.Close()

	// Route BOTH Auth and Drive through Transport backed by srv.Client().Do,
	// with Drive strictly limited to maxPending=1 to prove no semaphore deadlock!
	httpDo := func(ctx context.Context, req *HTTPRequest) (*HTTPResponse, error) {
		httpReq, err := http.NewRequestWithContext(ctx, req.Method, req.URL, strings.NewReader(string(req.Body)))
		if err != nil {
			return nil, err
		}
		for k, v := range req.Headers {
			httpReq.Header.Set(k, v)
		}
		resp, err := srv.Client().Do(httpReq)
		if err != nil {
			return nil, err
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		hdrs := map[string]string{}
		for k := range resp.Header {
			hdrs[k] = resp.Header.Get(k)
		}
		return &HTTPResponse{StatusCode: resp.StatusCode, Headers: hdrs, Body: body}, nil
	}

	authClient := &http.Client{Transport: NewTransport(httpDo, 0)}
	tm := NewTokenManager(authClient, srv.URL)
	tm.SetRefreshToken("rt-1")

	driveTransport := NewTransport(httpDo, 1)
	client := NewClient(&http.Client{Transport: driveTransport}, tm, noSleepRetryOptions())
	client.SetEndpoints(srv.URL+"/drive/v3", srv.URL+"/upload/drive/v3")

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			content, err := client.ReadFile(ctx, "f1")
			if err != nil {
				t.Errorf("ReadFile error: %v", err)
				return
			}
			if content != "note body" {
				t.Errorf("ReadFile = %q, want note body", content)
			}
		}()
	}
	wg.Wait()

	if driveCalls.Load() < 2 {
		t.Fatalf("expected 401 followed by retries, driveCalls=%d", driveCalls.Load())
	}
}

func TestClientFolderAndMultipartCRUD(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/google/token/refresh" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(tokenRefreshResponse{AccessToken: "tok", ExpiresIn: 3600})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/drive/v3/files":
			q := r.URL.Query().Get("q")
			if strings.Contains(q, "name='existing'") {
				_, _ = w.Write([]byte(`{"files":[{"id":"folder-existing","name":"existing","mimeType":"application/vnd.google-apps.folder"}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"files":[]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/drive/v3/files":
			_, _ = w.Write([]byte(`{"id":"folder-sub","name":"sub","mimeType":"application/vnd.google-apps.folder"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/upload/drive/v3/files":
			ct := r.Header.Get("Content-Type")
			if !strings.HasPrefix(ct, "multipart/related; boundary=") {
				t.Errorf("unexpected Content-Type: %s", ct)
			}
			raw, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(raw), "hello world") {
				t.Errorf("multipart body missing content: %s", string(raw))
			}
			_, _ = w.Write([]byte(`{"id":"file-new","name":"note.md","mimeType":"text/markdown"}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/upload/drive/v3/files/file-new":
			_, _ = w.Write([]byte(`{"id":"file-new","name":"note.md","mimeType":"text/markdown","modifiedTime":"2026-11-12T10:00:00Z"}`))
		default:
			http.Error(w, "unhandled "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	tm := NewTokenManager(srv.Client(), srv.URL)
	tm.SetRefreshToken("rt")
	client := NewClient(srv.Client(), tm, noSleepRetryOptions())
	client.SetEndpoints(srv.URL+"/drive/v3", srv.URL+"/upload/drive/v3")

	folderID, err := client.EnsureFolderPath(context.Background(), "/existing/sub")
	if err != nil || folderID != "folder-sub" {
		t.Fatalf("EnsureFolderPath = (%q, %v), want folder-sub", folderID, err)
	}

	created, err := client.CreateFile(context.Background(), folderID, "note.md", "hello world")
	if err != nil || created.ID != "file-new" {
		t.Fatalf("CreateFile = (%+v, %v)", created, err)
	}

	updated, err := client.UpdateFile(context.Background(), created.ID, "note.md", "updated content")
	if err != nil || updated.ModifiedTime == "" {
		t.Fatalf("UpdateFile = (%+v, %v)", updated, err)
	}
}
