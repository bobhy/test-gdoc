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

	"test-gdoc/go/gdrive"
	"test-gdoc/go/settings"
	"test-gdoc/go/syncplan"
	"test-gdoc/go/syncstore"
)

type fakeVaultFS struct {
	mu    sync.Mutex
	files map[string]string
}

func (f *fakeVaultFS) ListFlatStats(folder string) ([]settings.VaultFileStat, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	prefix := syncplan.NormalizeVaultPath(folder)
	var out []settings.VaultFileStat
	for p, body := range f.files {
		if strings.HasPrefix(p, prefix+"/") {
			rest := strings.TrimPrefix(p, prefix+"/")
			if !strings.Contains(rest, "/") {
				out = append(out, settings.VaultFileStat{
					Name:    rest,
					Path:    p,
					Size:    int64(len(body)),
					MTimeMs: 100,
				})
			}
		}
	}
	return out, nil
}

func (f *fakeVaultFS) Exists(path string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.files[syncplan.NormalizeVaultPath(path)]
	return ok, nil
}

func (f *fakeVaultFS) ReadText(path string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.files[syncplan.NormalizeVaultPath(path)]
	return v, ok, nil
}

func (f *fakeVaultFS) WriteText(path, content string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[syncplan.NormalizeVaultPath(path)] = content
	return nil
}

func (f *fakeVaultFS) RenameFile(oldPath, newPath string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	o := syncplan.NormalizeVaultPath(oldPath)
	n := syncplan.NormalizeVaultPath(newPath)
	if v, ok := f.files[o]; ok {
		delete(f.files, o)
		f.files[n] = v
	}
	return nil
}

func newTestServer(mu *sync.Mutex, driveFiles map[string]string, driveNames map[string]string) (*httptest.Server, gdrive.DoFunc) {
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
			mu.Lock()
			var items []settings.FileMeta
			for id, body := range driveFiles {
				name := driveNames[id]
				sha, md5Hex, _ := syncstore.ComputeChecksums(body)
				items = append(items, settings.FileMeta{
					ID:             id,
					Name:           name,
					MimeType:       "text/markdown",
					SHA256Checksum: sha,
					MD5Checksum:    md5Hex,
				})
			}
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"files": items})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/drive/v3/files/") && r.URL.Query().Get("alt") == "media":
			id := strings.TrimPrefix(r.URL.Path, "/drive/v3/files/")
			mu.Lock()
			content := driveFiles[id]
			mu.Unlock()
			w.Header().Set("Content-Type", "text/markdown")
			_, _ = w.Write([]byte(content))
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/drive/v3/files/"):
			id := strings.TrimPrefix(r.URL.Path, "/drive/v3/files/")
			var metaPatch struct {
				Name string `json:"name"`
			}
			_ = json.NewDecoder(r.Body).Decode(&metaPatch)
			mu.Lock()
			if metaPatch.Name != "" {
				driveNames[id] = metaPatch.Name
			}
			sha, md5Hex, _ := syncstore.ComputeChecksums(driveFiles[id])
			name := driveNames[id]
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(settings.FileMeta{
				ID: id, Name: name, MimeType: "text/markdown", SHA256Checksum: sha, MD5Checksum: md5Hex,
			})
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/upload/drive/v3/files/"):
			id := strings.TrimPrefix(r.URL.Path, "/upload/drive/v3/files/")
			raw, _ := io.ReadAll(r.Body)
			s := string(raw)
			idx := strings.LastIndex(s, "\r\n\r\n")
			endIdx := strings.LastIndex(s, "\r\n--")
			mu.Lock()
			if idx > 0 && endIdx > idx {
				driveFiles[id] = s[idx+4 : endIdx]
			}
			sha, md5Hex, _ := syncstore.ComputeChecksums(driveFiles[id])
			name := driveNames[id]
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(settings.FileMeta{
				ID: id, Name: name, MimeType: "text/markdown", SHA256Checksum: sha, MD5Checksum: md5Hex,
			})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))

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
	return srv, doFunc
}

func TestBridgeServiceHybridPullPushWithSyncStore(t *testing.T) {
	var mu sync.Mutex
	driveFiles := map[string]string{
		"f-a": "line 1\nline 2\nline 3",
		"f-b": "beta remote",
	}
	driveNames := map[string]string{
		"f-a": "a.md",
		"f-b": "b.txt",
	}
	srv, doFunc := newTestServer(&mu, driveFiles, driveNames)
	defer srv.Close()

	store := syncstore.NewMemoryStore()
	svc := NewBridgeService(doFunc, gdrive.RetryOptions{MaxAttempts: 2}, store, nil)
	svc.auth = gdrive.NewTokenManager(&http.Client{Transport: gdrive.NewTransport(doFunc, 0)}, srv.URL)
	svc.client = gdrive.NewClient(&http.Client{Transport: gdrive.NewTransport(doFunc, 3)}, svc.auth, gdrive.RetryOptions{MaxAttempts: 2})
	svc.client.SetEndpoints(srv.URL+"/drive/v3", srv.URL+"/upload/drive/v3")

	cfgPayload, _ := json.Marshal(settings.ConfigureRequest{RefreshToken: "rt-1", MaxPendingRequests: 3, VaultName: "v1"})
	if _, err := svc.Handle(context.Background(), "configure", string(cfgPayload)); err != nil {
		t.Fatalf("configure failed: %v", err)
	}

	vault := &fakeVaultFS{files: map[string]string{}}
	pullRaw, err := svc.Handle(context.Background(), "pullPrepare", `{"folderPath":"/test-gdoc"}`)
	if err != nil {
		t.Fatalf("pullPrepare: %v", err)
	}
	pullPrep := pullRaw.(settings.PullPrepareResult)
	pullSum, err := svc.ApplyPullBatch(vault, pullPrep.BatchID, "sync")
	if err != nil || pullSum.Pulled != 2 || pullSum.UpdatedIDB != 2 {
		t.Fatalf("ApplyPullBatch = %+v err=%v", pullSum, err)
	}

	// Verify base stored inside syncstore without any Vault base folder.
	baseA, _, hasBase, err := store.GetBase(context.Background(), "a.md")
	if err != nil || !hasBase || baseA != "line 1\nline 2\nline 3" {
		t.Fatalf("expected stored base in SyncStore, got hasBase=%v base=%q err=%v", hasBase, baseA, err)
	}

	// Simulate non-overlapping concurrent edits (local line 1, remote line 3) and conflict-marked merge.
	vault.files["sync/a.md"] = "local 1\nline 2\nline 3"
	mu.Lock()
	driveFiles["f-a"] = "line 1\nline 2\nremote 3"
	mu.Unlock()

	batchID, err := svc.PreparePushInputs(vault, "sync", "a.md")
	if err != nil {
		t.Fatalf("PreparePushInputs: %v", err)
	}
	execPayload, _ := json.Marshal(settings.PushExecuteRequest{FolderPath: "/test-gdoc", BatchID: batchID})
	execRaw, err := svc.Handle(context.Background(), "executePreparedPush", string(execPayload))
	if err != nil {
		t.Fatalf("executePreparedPush: %v", err)
	}
	execRes := execRaw.(settings.PushExecuteResult)
	if execRes.Outcome != "clean" {
		t.Fatalf("outcome = %q, want clean", execRes.Outcome)
	}
	applySum, err := svc.ApplyPreparedPush(vault, batchID)
	if err != nil || applySum.Outcome != "clean" {
		t.Fatalf("ApplyPreparedPush = %+v err=%v", applySum, err)
	}
	if vault.files["sync/a.md"] != "local 1\nline 2\nremote 3" {
		t.Fatalf("merged local = %q", vault.files["sync/a.md"])
	}

	// Verify scan + base inspector RPCs.
	scanID, err := svc.CollectVaultStats(vault, "sync")
	if err != nil {
		t.Fatalf("CollectVaultStats: %v", err)
	}
	prepPayload, _ := json.Marshal(prepareScanPayload{
		ScanID: scanID, DriveFolderPath: "/test-gdoc", LocalSyncFolderPath: "sync", IncludeDrive: true,
	})
	if _, err := svc.Handle(context.Background(), "prepareVaultScan", string(prepPayload)); err != nil {
		t.Fatalf("prepareVaultScan: %v", err)
	}
	if err := svc.ReadVaultFilesForScan(vault, scanID); err != nil {
		t.Fatalf("ReadVaultFilesForScan: %v", err)
	}
	commitPayload, _ := json.Marshal(commitScanPayload{ScanID: scanID})
	stateRaw, err := svc.Handle(context.Background(), "commitSyncScan", string(commitPayload))
	if err != nil {
		t.Fatalf("commitSyncScan: %v", err)
	}
	stateRes := stateRaw.(settings.GetSyncStateResult)
	if len(stateRes.Records) != 2 {
		t.Fatalf("expected 2 SyncRecords, got %+v", stateRes.Records)
	}

	if _, err := svc.Handle(context.Background(), "clearBaseSnapshot", `{"fileName":"b.txt"}`); err != nil {
		t.Fatalf("clearBaseSnapshot: %v", err)
	}
	getBaseRaw, err := svc.Handle(context.Background(), "getBaseSnapshot", `{"fileName":"b.txt"}`)
	if err != nil {
		t.Fatalf("getBaseSnapshot: %v", err)
	}
	if getBaseRaw.(settings.BaseSnapshotResult).HasBase {
		t.Fatal("expected b.txt base to be cleared")
	}

	benchRaw, err := svc.Handle(context.Background(), "runIdbBenchmark", `{"count":10}`)
	if err != nil {
		t.Fatalf("runIdbBenchmark: %v", err)
	}
	if benchRaw.(settings.IDBBenchResult).Count != 10 {
		t.Fatalf("unexpected bench result: %+v", benchRaw)
	}
}
