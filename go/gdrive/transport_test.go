package gdrive

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTransportRoundTrip(t *testing.T) {
	tr := NewTransport(func(_ context.Context, req *HTTPRequest) (*HTTPResponse, error) {
		if req.Method != http.MethodPost {
			t.Errorf("Method = %s, want POST", req.Method)
		}
		if req.URL != "https://example.test/upload" {
			t.Errorf("URL = %s", req.URL)
		}
		if req.Headers["X-Custom"] != "alpha" {
			t.Errorf("X-Custom = %q, want alpha", req.Headers["X-Custom"])
		}
		if string(req.Body) != "hello-bytes" {
			t.Errorf("Body = %q, want hello-bytes", string(req.Body))
		}
		return &HTTPResponse{
			StatusCode: http.StatusCreated,
			Headers:    map[string]string{"x-reply": "pong"},
			Body:       []byte("ok-response"),
		}, nil
	}, 2)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://example.test/upload", bytes.NewBufferString("hello-bytes"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Custom", "alpha")

	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated {
		t.Errorf("StatusCode = %d, want 201", resp.StatusCode)
	}
	if resp.Header.Get("X-Reply") != "pong" {
		t.Errorf("X-Reply = %q, want pong", resp.Header.Get("X-Reply"))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "ok-response" {
		t.Errorf("Body = %q, want ok-response", string(body))
	}
}

func TestTransportConcurrencyCap(t *testing.T) {
	var current atomic.Int32
	var maxSeen atomic.Int32

	tr := NewTransport(func(_ context.Context, _ *HTTPRequest) (*HTTPResponse, error) {
		cur := current.Add(1)
		for {
			prev := maxSeen.Load()
			if cur <= prev || maxSeen.CompareAndSwap(prev, cur) {
				break
			}
		}
		time.Sleep(15 * time.Millisecond)
		current.Add(-1)
		return &HTTPResponse{StatusCode: http.StatusOK}, nil
	}, 2)

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://example.test", nil)
			resp, err := tr.RoundTrip(req)
			if err != nil {
				t.Errorf("RoundTrip error: %v", err)
				return
			}
			_ = resp.Body.Close()
		}()
	}
	wg.Wait()

	if got := maxSeen.Load(); got > 2 || got < 2 {
		t.Fatalf("maxConcurrentObserved = %d, want 2", got)
	}
}

func TestTransportRespectsContextWhileWaitingForSlot(t *testing.T) {
	started := make(chan struct{})
	releaseFirst := make(chan struct{})

	tr := NewTransport(func(_ context.Context, _ *HTTPRequest) (*HTTPResponse, error) {
		close(started)
		<-releaseFirst
		return &HTTPResponse{StatusCode: http.StatusOK}, nil
	}, 1)

	doneFirst := make(chan struct{})
	go func() {
		defer close(doneFirst)
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://example.test/1", nil)
		resp, err := tr.RoundTrip(req)
		if err == nil {
			_ = resp.Body.Close()
		}
	}()

	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	req2, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.test/2", nil)
	_, err := tr.RoundTrip(req2)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded, got %v", err)
	}
	close(releaseFirst)
	<-doneFirst
}
