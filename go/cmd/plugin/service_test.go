package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"test-gdoc/go/gdrive"
	"test-gdoc/go/settings"
)

func TestBridgeServiceMergeAndPushFlow(t *testing.T) {
	var mu sync.Mutex
	driveFiles := map[string]string{
		"f-note": "line 1\nline 2\nline 3",
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/google/token/refresh" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"tok","expires_in":3600}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/drive/v3/files":
			q := r.URL.Query().Get("q")
			if strings.Contains(q, "mimeType='application/vnd.google-apps.folder'") {
				_, _ = w.Write([]byte(`{"files":[{"id":"folder-1","name":"test-gdoc","mimeType":"application/vnd.google-apps.folder"}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"files":[{"id":"f-note","name":"note.md","mimeType":"text/markdown"}]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/drive/v3/files/f-note" && r.URL.Query().Get("alt") == "media":
			mu.Lock()
			content := driveFiles["f-note"]
			mu.Unlock()
			w.Header().Set("Content-Type", "text/markdown")
			_, _ = w.Write([]byte(content))
		case r.Method == http.MethodPatch && r.URL.Path == "/upload/drive/v3/files/f-note":
			raw, _ := io.ReadAll(r.Body)
			// Extract uploaded content after blank line in multipart media section for assertion.
			s := string(raw)
			idx := strings.LastIndex(s, "\r\n\r\n")
			endIdx := strings.LastIndex(s, "\r\n--")
			if idx > 0 && endIdx > idx {
				mu.Lock()
				driveFiles["f-note"] = s[idx+4 : endIdx]
				mu.Unlock()
			}
			_, _ = w.Write([]byte(`{"id":"f-note","name":"note.md","mimeType":"text/markdown"}`))
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	doFunc := func(ctx context.Context, req *gdrive.HTTPRequest) (*gdrive.HTTPResponse, error) {
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
		b, _ := io.ReadAll(resp.Body)
		hdrs := map[string]string{}
		for k := range resp.Header {
			hdrs[k] = resp.Header.Get(k)
		}
		return &gdrive.HTTPResponse{StatusCode: resp.StatusCode, Headers: hdrs, Body: b}, nil
	}

	svc := NewBridgeService(doFunc, gdrive.RetryOptions{
		MaxAttempts: 2,
		BaseDelay:   time.Millisecond,
		MaxDelay:    time.Millisecond,
		Sleep:       func(_ context.Context, _ time.Duration) error { return nil },
		Jitter:      func(_ time.Duration) time.Duration { return 0 },
	})
	svc.auth = gdrive.NewTokenManager(&http.Client{Transport: gdrive.NewTransport(doFunc, 0)}, srv.URL)
	svc.client = gdrive.NewClient(&http.Client{Transport: gdrive.NewTransport(doFunc, 3)}, svc.auth, gdrive.RetryOptions{MaxAttempts: 2})
	svc.client.SetEndpoints(srv.URL+"/drive/v3", srv.URL+"/upload/drive/v3")

	cfgPayload, _ := json.Marshal(settings.ConfigureRequest{RefreshToken: "rt-1", MaxPendingRequests: 2})
	if _, err := svc.Handle(context.Background(), "configure", string(cfgPayload)); err != nil {
		t.Fatalf("configure failed: %v", err)
	}

	// Simulate remote edit on line 3 while local edited line 1.
	mu.Lock()
	driveFiles["f-note"] = "line 1\nline 2\nremote 3"
	mu.Unlock()

	pushPayload, _ := json.Marshal(settings.MergeAndPushRequest{
		FolderPath:   "/test-gdoc",
		FileName:     "note.md",
		BaseContent:  "line 1\nline 2\nline 3",
		LocalContent: "local 1\nline 2\nline 3",
		HasBase:      true,
	})
	rawRes, err := svc.Handle(context.Background(), "mergeAndPushFile", string(pushPayload))
	if err != nil {
		t.Fatalf("mergeAndPushFile failed: %v", err)
	}
	res := rawRes.(settings.MergeAndPushResult)
	if res.Outcome != "clean" || !res.DriveUpdated || !res.LocalUpdated {
		t.Fatalf("unexpected MergeAndPushResult: %+v", res)
	}
	if res.Content != "local 1\nline 2\nremote 3" {
		t.Fatalf("merged Content = %q", res.Content)
	}
}
