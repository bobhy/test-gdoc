// Package gdrive implements a pure-Go Google Drive v3 client, OAuth token
// refresher, concurrency limiter, and RoundTripper transport adapter.
// It contains no syscall/js imports so it can be unit-tested natively with go test.
package gdrive

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

// HTTPRequest represents a single buffered outbound HTTP request passed across the bridge.
type HTTPRequest struct {
	Method  string
	URL     string
	Headers map[string]string
	Body    []byte
}

// HTTPResponse represents a single buffered HTTP response returned across the bridge.
type HTTPResponse struct {
	StatusCode int
	Headers    map[string]string
	Body       []byte
}

// DoFunc executes a buffered HTTP request (in Wasm, bridged to Obsidian's requestUrl).
type DoFunc func(ctx context.Context, req *HTTPRequest) (*HTTPResponse, error)

// Transport implements http.RoundTripper over a pluggable DoFunc with optional
// concurrency limiting via a dynamic counting semaphore.
//
// Only individual RoundTrip calls hold a concurrency permit; retry/backoff sleeps
// happen in the caller after RoundTrip returns and releases the permit.
type Transport struct {
	doFunc DoFunc

	mu         sync.Mutex
	maxPending int
	inFlight   int
	waitCh     chan struct{}
}

// NewTransport constructs a Transport with the given DoFunc and maxPending concurrency limit.
// A maxPending <= 0 disables concurrency throttling (used for OAuth token refresh).
func NewTransport(doFunc DoFunc, maxPending int) *Transport {
	return &Transport{
		doFunc:     doFunc,
		maxPending: maxPending,
	}
}

// SetMaxPending updates the concurrency limit dynamically and wakes waiting callers.
func (t *Transport) SetMaxPending(maxPending int) {
	t.mu.Lock()
	t.maxPending = maxPending
	if t.waitCh != nil {
		close(t.waitCh)
		t.waitCh = nil
	}
	t.mu.Unlock()
}

// RoundTrip executes req via DoFunc, gating execution with the concurrency semaphore when enabled.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("gdrive: nil http.Request")
	}
	if t.doFunc == nil {
		return nil, fmt.Errorf("gdrive: nil DoFunc")
	}

	if err := t.acquire(req.Context()); err != nil {
		return nil, err
	}
	defer t.release()

	var bodyBytes []byte
	if req.Body != nil && req.Body != http.NoBody {
		var err error
		bodyBytes, err = io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("gdrive: read request body: %w", err)
		}
	}

	headers := make(map[string]string, len(req.Header))
	for k, vals := range req.Header {
		headers[k] = strings.Join(vals, ", ")
	}

	resp, err := t.doFunc(req.Context(), &HTTPRequest{
		Method:  req.Method,
		URL:     req.URL.String(),
		Headers: headers,
		Body:    bodyBytes,
	})
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, fmt.Errorf("gdrive: nil HTTPResponse from DoFunc")
	}

	httpHeader := make(http.Header, len(resp.Headers))
	for k, v := range resp.Headers {
		httpHeader.Set(k, v)
	}

	return &http.Response{
		StatusCode:    resp.StatusCode,
		Status:        fmt.Sprintf("%d %s", resp.StatusCode, http.StatusText(resp.StatusCode)),
		Header:        httpHeader,
		Body:          io.NopCloser(bytes.NewReader(resp.Body)),
		ContentLength: int64(len(resp.Body)),
		Request:       req,
	}, nil
}

func (t *Transport) acquire(ctx context.Context) error {
	for {
		t.mu.Lock()
		if t.maxPending <= 0 || t.inFlight < t.maxPending {
			t.inFlight++
			t.mu.Unlock()
			return nil
		}
		ch := t.waitCh
		if ch == nil {
			ch = make(chan struct{})
			t.waitCh = ch
		}
		t.mu.Unlock()

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ch:
		}
	}
}

func (t *Transport) release() {
	t.mu.Lock()
	if t.inFlight > 0 {
		t.inFlight--
	}
	if t.waitCh != nil {
		close(t.waitCh)
		t.waitCh = nil
	}
	t.mu.Unlock()
}
