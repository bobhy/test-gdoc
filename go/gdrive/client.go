package gdrive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"test-gdoc/go/settings"
)

const (
	defaultDriveAPIBase   = "https://www.googleapis.com/drive/v3"
	defaultDriveUploadAPI = "https://www.googleapis.com/upload/drive/v3"
	folderMimeType        = "application/vnd.google-apps.folder"
	fileFields            = "id,name,mimeType,modifiedTime,size"
)

// RetryOptions configures exponential backoff retries for transient Drive API errors.
type RetryOptions struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
	Sleep       func(ctx context.Context, d time.Duration) error
	Jitter      func(base time.Duration) time.Duration
}

// DefaultRetryOptions returns production backoff defaults (~0.5s doubling up to 30s across 6 attempts).
func DefaultRetryOptions() RetryOptions {
	return RetryOptions{
		MaxAttempts: 6,
		BaseDelay:   500 * time.Millisecond,
		MaxDelay:    30 * time.Second,
		Sleep:       sleepContext,
		Jitter: func(base time.Duration) time.Duration {
			if base <= 0 {
				return 0
			}
			return time.Duration(rand.Int64N(int64(base)))
		},
	}
}

// APIError represents a non-2xx response returned by Google Drive v3.
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	return e.Message
}

// Client calls the Google Drive v3 REST API over a semaphore-capped *http.Client.
type Client struct {
	httpClient   *http.Client
	auth         *TokenManager
	apiBase      string
	uploadBase   string
	retry        RetryOptions
}

// NewClient constructs a Drive v3 Client with the provided HTTP client and TokenManager.
func NewClient(httpClient *http.Client, auth *TokenManager, retry RetryOptions) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	def := DefaultRetryOptions()
	if retry.MaxAttempts <= 0 {
		retry.MaxAttempts = def.MaxAttempts
	}
	if retry.BaseDelay <= 0 {
		retry.BaseDelay = def.BaseDelay
	}
	if retry.MaxDelay <= 0 {
		retry.MaxDelay = def.MaxDelay
	}
	if retry.Sleep == nil {
		retry.Sleep = def.Sleep
	}
	if retry.Jitter == nil {
		retry.Jitter = def.Jitter
	}
	return &Client{
		httpClient: httpClient,
		auth:       auth,
		apiBase:    defaultDriveAPIBase,
		uploadBase: defaultDriveUploadAPI,
		retry:      retry,
	}
}

// SetEndpoints overrides the Drive API and upload base URLs (used in tests).
func (c *Client) SetEndpoints(apiBase, uploadBase string) {
	c.apiBase = strings.TrimRight(apiBase, "/")
	c.uploadBase = strings.TrimRight(uploadBase, "/")
}

// IsEligibleFileName returns true for flat .md and .txt filenames supported by test-gdoc.
func IsEligibleFileName(name string) bool {
	ext := strings.ToLower(path.Ext(strings.TrimSpace(name)))
	return ext == ".md" || ext == ".txt"
}

type driveFileListResponse struct {
	Files         []settings.FileMeta `json:"files"`
	NextPageToken string              `json:"nextPageToken"`
}

// EnsureFolderPath resolves a slash-separated path from My Drive root, creating any missing folders.
func (c *Client) EnsureFolderPath(ctx context.Context, folderPath string) (string, error) {
	segments := splitFolderPath(folderPath)
	if len(segments) == 0 {
		return "root", nil
	}

	parentID := "root"
	for _, seg := range segments {
		existing, err := c.findChild(ctx, parentID, seg, true)
		if err != nil {
			return "", err
		}
		if existing != nil {
			parentID = existing.ID
			continue
		}
		created, err := c.createFolder(ctx, parentID, seg)
		if err != nil {
			return "", err
		}
		parentID = created.ID
	}
	return parentID, nil
}

// ListFiles lists all non-trashed flat .md/.txt files directly inside folderID.
func (c *Client) ListFiles(ctx context.Context, folderID string) ([]settings.FileMeta, error) {
	q := fmt.Sprintf("'%s' in parents and mimeType!='%s' and trashed=false", escapeQueryLiteral(folderID), folderMimeType)
	fields := "nextPageToken,files(" + fileFields + ")"
	baseURL := fmt.Sprintf(
		"%s/files?q=%s&fields=%s&pageSize=1000&orderBy=%s",
		c.apiBase,
		url.QueryEscape(q),
		url.QueryEscape(fields),
		url.QueryEscape("modifiedTime desc"),
	)

	files := make([]settings.FileMeta, 0)
	pageToken := ""
	for {
		reqURL := baseURL
		if pageToken != "" {
			reqURL += "&pageToken=" + url.QueryEscape(pageToken)
		}
		status, body, err := c.doAuthenticated(ctx, http.MethodGet, reqURL, nil, nil)
		if err != nil {
			return nil, err
		}
		if err := checkStatus(status, body); err != nil {
			return nil, err
		}
		var page driveFileListResponse
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("parse Drive file list: %w", err)
		}
		for _, f := range page.Files {
			if IsEligibleFileName(f.Name) {
				files = append(files, f)
			}
		}
		if page.NextPageToken == "" {
			break
		}
		pageToken = page.NextPageToken
	}
	return files, nil
}

// GetFile fetches metadata for a single Drive file by ID.
func (c *Client) GetFile(ctx context.Context, fileID string) (settings.FileMeta, error) {
	reqURL := fmt.Sprintf("%s/files/%s?fields=%s", c.apiBase, url.PathEscape(fileID), url.QueryEscape(fileFields))
	status, body, err := c.doAuthenticated(ctx, http.MethodGet, reqURL, nil, nil)
	if err != nil {
		return settings.FileMeta{}, err
	}
	if err := checkStatus(status, body); err != nil {
		return settings.FileMeta{}, err
	}
	var meta settings.FileMeta
	if err := json.Unmarshal(body, &meta); err != nil {
		return settings.FileMeta{}, fmt.Errorf("parse Drive file metadata: %w", err)
	}
	return meta, nil
}

// ReadFile downloads a file's UTF-8 content by file ID.
func (c *Client) ReadFile(ctx context.Context, fileID string) (string, error) {
	reqURL := fmt.Sprintf("%s/files/%s?alt=media", c.apiBase, url.PathEscape(fileID))
	status, body, err := c.doAuthenticated(ctx, http.MethodGet, reqURL, nil, nil)
	if err != nil {
		return "", err
	}
	if err := checkStatus(status, body); err != nil {
		return "", err
	}
	return string(body), nil
}

// FindFile looks up a non-folder file by exact name inside folderID. Returns nil if not found.
func (c *Client) FindFile(ctx context.Context, folderID, name string) (*settings.FileMeta, error) {
	return c.findChild(ctx, folderID, name, false)
}

// CreateFile uploads a new text file to folderID using multipart/related upload.
func (c *Client) CreateFile(ctx context.Context, folderID, name, content string) (settings.FileMeta, error) {
	mimeType := contentMimeType(name)
	metaPayload := map[string]any{
		"name":    name,
		"parents": []string{folderID},
	}
	body, contentType, err := buildMultipartRelated(metaPayload, content, mimeType)
	if err != nil {
		return settings.FileMeta{}, err
	}
	reqURL := fmt.Sprintf("%s/files?uploadType=multipart&fields=%s", c.uploadBase, url.QueryEscape(fileFields))
	status, respBody, err := c.doAuthenticated(ctx, http.MethodPost, reqURL, map[string]string{"Content-Type": contentType}, body)
	if err != nil {
		return settings.FileMeta{}, err
	}
	if err := checkStatus(status, respBody); err != nil {
		return settings.FileMeta{}, err
	}
	var created settings.FileMeta
	if err := json.Unmarshal(respBody, &created); err != nil {
		return settings.FileMeta{}, fmt.Errorf("parse created Drive file: %w", err)
	}
	return created, nil
}

// UpdateFile overwrites an existing Drive file's content using multipart/related PATCH upload.
func (c *Client) UpdateFile(ctx context.Context, fileID, name, content string) (settings.FileMeta, error) {
	mimeType := contentMimeType(name)
	body, contentType, err := buildMultipartRelated(map[string]any{}, content, mimeType)
	if err != nil {
		return settings.FileMeta{}, err
	}
	reqURL := fmt.Sprintf("%s/files/%s?uploadType=multipart&fields=%s", c.uploadBase, url.PathEscape(fileID), url.QueryEscape(fileFields))
	status, respBody, err := c.doAuthenticated(ctx, http.MethodPatch, reqURL, map[string]string{"Content-Type": contentType}, body)
	if err != nil {
		return settings.FileMeta{}, err
	}
	if err := checkStatus(status, respBody); err != nil {
		return settings.FileMeta{}, err
	}
	var updated settings.FileMeta
	if err := json.Unmarshal(respBody, &updated); err != nil {
		return settings.FileMeta{}, fmt.Errorf("parse updated Drive file: %w", err)
	}
	return updated, nil
}

func (c *Client) findChild(ctx context.Context, parentID, name string, wantFolder bool) (*settings.FileMeta, error) {
	mimeClause := "mimeType!='" + folderMimeType + "'"
	if wantFolder {
		mimeClause = "mimeType='" + folderMimeType + "'"
	}
	q := fmt.Sprintf("'%s' in parents and name='%s' and %s and trashed=false", escapeQueryLiteral(parentID), escapeQueryLiteral(name), mimeClause)
	reqURL := fmt.Sprintf(
		"%s/files?q=%s&fields=%s&pageSize=2&orderBy=%s",
		c.apiBase,
		url.QueryEscape(q),
		url.QueryEscape("files("+fileFields+")"),
		url.QueryEscape("modifiedTime desc"),
	)
	status, body, err := c.doAuthenticated(ctx, http.MethodGet, reqURL, nil, nil)
	if err != nil {
		return nil, err
	}
	if err := checkStatus(status, body); err != nil {
		return nil, err
	}
	var list driveFileListResponse
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("parse Drive child search: %w", err)
	}
	if len(list.Files) == 0 {
		return nil, nil
	}
	found := list.Files[0]
	return &found, nil
}

func (c *Client) createFolder(ctx context.Context, parentID, name string) (settings.FileMeta, error) {
	payload, err := json.Marshal(map[string]any{
		"name":     name,
		"mimeType": folderMimeType,
		"parents":  []string{parentID},
	})
	if err != nil {
		return settings.FileMeta{}, err
	}
	reqURL := fmt.Sprintf("%s/files?fields=%s", c.apiBase, url.QueryEscape(fileFields))
	status, body, err := c.doAuthenticated(ctx, http.MethodPost, reqURL, map[string]string{"Content-Type": "application/json"}, payload)
	if err != nil {
		return settings.FileMeta{}, err
	}
	if err := checkStatus(status, body); err != nil {
		return settings.FileMeta{}, err
	}
	var folder settings.FileMeta
	if err := json.Unmarshal(body, &folder); err != nil {
		return settings.FileMeta{}, fmt.Errorf("parse created Drive folder: %w", err)
	}
	return folder, nil
}

// doAuthenticated acquires an access token outside the Drive semaphore, executes
// the request with transient retry/backoff, and performs a single 401 refresh retry.
func (c *Client) doAuthenticated(
	ctx context.Context,
	method string,
	reqURL string,
	extraHeaders map[string]string,
	body []byte,
) (int, []byte, error) {
	status, respBody, err := c.requestWithRetry(ctx, method, reqURL, extraHeaders, body)
	if err == nil && status == http.StatusUnauthorized {
		c.auth.Invalidate()
		return c.requestWithRetry(ctx, method, reqURL, extraHeaders, body)
	}
	return status, respBody, err
}

func (c *Client) requestWithRetry(
	ctx context.Context,
	method string,
	reqURL string,
	extraHeaders map[string]string,
	body []byte,
) (int, []byte, error) {
	var lastErr error
	for attempt := 1; attempt <= c.retry.MaxAttempts; attempt++ {
		if ctx.Err() != nil {
			return 0, nil, ctx.Err()
		}

		// Always fetch/refresh the token BEFORE entering Transport's semaphore so
		// token refresh requests never contend with Drive concurrency slots.
		token, err := c.auth.AccessToken(ctx)
		if err != nil {
			return 0, nil, err
		}

		var bodyReader io.Reader
		if len(body) > 0 {
			bodyReader = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, reqURL, bodyReader)
		if err != nil {
			return 0, nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		for k, v := range extraHeaders {
			req.Header.Set(k, v)
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil || attempt >= c.retry.MaxAttempts {
				return 0, nil, lastErr
			}
			delay := c.backoffDelay(attempt, nil)
			if sleepErr := c.retry.Sleep(ctx, delay); sleepErr != nil {
				return 0, nil, sleepErr
			}
			continue
		}

		respBody, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			if attempt >= c.retry.MaxAttempts {
				return 0, nil, lastErr
			}
			delay := c.backoffDelay(attempt, resp.Header)
			if sleepErr := c.retry.Sleep(ctx, delay); sleepErr != nil {
				return 0, nil, sleepErr
			}
			continue
		}

		if !isRetryableResponse(resp.StatusCode, respBody) || attempt >= c.retry.MaxAttempts {
			return resp.StatusCode, respBody, nil
		}

		lastErr = &APIError{
			StatusCode: resp.StatusCode,
			Message:    fmt.Sprintf("Drive API transient error (%d)", resp.StatusCode),
		}
		delay := c.backoffDelay(attempt, resp.Header)
		if sleepErr := c.retry.Sleep(ctx, delay); sleepErr != nil {
			return 0, nil, sleepErr
		}
	}

	if lastErr != nil {
		return 0, nil, lastErr
	}
	return 0, nil, errors.New("Drive request failed after retries")
}

func (c *Client) backoffDelay(attempt int, headers http.Header) time.Duration {
	if headers != nil {
		if d, ok := parseRetryAfter(headers.Get("Retry-After"), time.Now()); ok {
			if d > c.retry.MaxDelay {
				return c.retry.MaxDelay
			}
			return d
		}
	}
	exp := c.retry.BaseDelay
	for i := 1; i < attempt; i++ {
		if exp >= c.retry.MaxDelay/2 {
			exp = c.retry.MaxDelay
			break
		}
		exp *= 2
	}
	jitter := c.retry.Jitter(c.retry.BaseDelay)
	delay := exp + jitter
	if delay > c.retry.MaxDelay {
		return c.retry.MaxDelay
	}
	return delay
}

func parseRetryAfter(raw string, now time.Time) (time.Duration, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	if secs, err := strconv.ParseFloat(raw, 64); err == nil {
		if secs <= 0 {
			return 0, true
		}
		return time.Duration(secs * float64(time.Second)), true
	}
	if when, err := http.ParseTime(raw); err == nil {
		d := when.Sub(now)
		if d < 0 {
			d = 0
		}
		return d, true
	}
	return 0, false
}

type driveErrorEnvelope struct {
	Error struct {
		Errors []struct {
			Reason string `json:"reason"`
		} `json:"errors"`
	} `json:"error"`
}

func isRetryableResponse(status int, body []byte) bool {
	switch status {
	case http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	case http.StatusForbidden:
		var env driveErrorEnvelope
		if err := json.Unmarshal(body, &env); err != nil {
			return false
		}
		for _, item := range env.Error.Errors {
			if item.Reason == "rateLimitExceeded" || item.Reason == "userRateLimitExceeded" {
				return true
			}
		}
		return false
	default:
		return false
	}
}

func checkStatus(status int, body []byte) error {
	if status >= 200 && status < 300 {
		return nil
	}
	return &APIError{
		StatusCode: status,
		Message:    fmt.Sprintf("Drive API error (%d): %s", status, strings.TrimSpace(string(body))),
	}
}

func splitFolderPath(folderPath string) []string {
	parts := strings.FieldsFunc(folderPath, func(r rune) bool {
		return r == '/' || r == '\\'
	})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func escapeQueryLiteral(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, `'`, `\'`)
}

func contentMimeType(name string) string {
	if strings.EqualFold(path.Ext(strings.TrimSpace(name)), ".md") {
		return "text/markdown"
	}
	return "text/plain"
}

func buildMultipartRelated(metadata map[string]any, content, mimeType string) ([]byte, string, error) {
	metaBytes, err := json.Marshal(metadata)
	if err != nil {
		return nil, "", err
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	metaHeader := textproto.MIMEHeader{}
	metaHeader.Set("Content-Type", "application/json; charset=UTF-8")
	metaPart, err := mw.CreatePart(metaHeader)
	if err != nil {
		return nil, "", err
	}
	if _, err := metaPart.Write(metaBytes); err != nil {
		return nil, "", err
	}

	mediaHeader := textproto.MIMEHeader{}
	mediaHeader.Set("Content-Type", mimeType)
	mediaPart, err := mw.CreatePart(mediaHeader)
	if err != nil {
		return nil, "", err
	}
	if _, err := io.WriteString(mediaPart, content); err != nil {
		return nil, "", err
	}

	if err := mw.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), "multipart/related; boundary=" + mw.Boundary(), nil
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
