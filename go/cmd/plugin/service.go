package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"test-gdoc/go/gdrive"
	"test-gdoc/go/merge"
	"test-gdoc/go/settings"
	"test-gdoc/go/syncplan"
	"test-gdoc/go/syncstore"
)

type preparedPull struct {
	driveFiles []settings.FileMeta
	pulled     []syncplan.PulledFile
	updatedIDB int
}

type preparedPush struct {
	fileName     string
	syncFolder   string
	vaultPath    string
	localContent string
	mtimeMs      int64
	res          settings.MergeAndPushResult
	executed     bool
}

type preparedScan struct {
	syncFolder   string
	driveFolder  string
	includeDrive bool
	vaultStats   []settings.VaultFileStat
	pathsToRead  []string
	fileContents map[string]string
	driveFiles   []settings.FileMeta
	rotatedToken string
}

// BridgeService routes typed JSON RPC calls and JSPI batch state between TypeScript,
// the pure-Go gdrive/merge/syncplan/syncstore packages, and IndexedDB.
type BridgeService struct {
	auth           *gdrive.TokenManager
	client         *gdrive.Client
	driveTransport *gdrive.Transport
	store          syncstore.Store
	onVaultName    func(string)
	now            func() time.Time

	pendingMu     sync.Mutex
	nextBatchID   int
	pendingPulls  map[int]*preparedPull
	pendingPushes map[int]*preparedPush
	pendingScans  map[int]*preparedScan
}

// NewBridgeService constructs a BridgeService backed by the provided transport and SyncStore.
func NewBridgeService(
	doFunc gdrive.DoFunc,
	retry gdrive.RetryOptions,
	store syncstore.Store,
	onVaultName func(string),
) *BridgeService {
	if store == nil {
		store = syncstore.NewMemoryStore()
	}
	authTransport := gdrive.NewTransport(doFunc, 0)
	authClient := &http.Client{Transport: authTransport}
	tm := gdrive.NewTokenManager(authClient, gdrive.DefaultRelayBaseURL)

	driveTransport := gdrive.NewTransport(doFunc, settings.DefaultSettings().MaxPendingRequests)
	driveClient := gdrive.NewClient(&http.Client{Transport: driveTransport}, tm, retry)

	return &BridgeService{
		auth:           tm,
		client:         driveClient,
		driveTransport: driveTransport,
		store:          store,
		onVaultName:    onVaultName,
		now:            time.Now,
		pendingPulls:   make(map[int]*preparedPull),
		pendingPushes:  make(map[int]*preparedPush),
		pendingScans:   make(map[int]*preparedScan),
	}
}

type prepareScanPayload struct {
	ScanID              int    `json:"scanId"`
	DriveFolderPath     string `json:"driveFolderPath"`
	LocalSyncFolderPath string `json:"localSyncFolderPath"`
	IncludeDrive        bool   `json:"includeDrive"`
}

type prepareScanResponse struct {
	ScanID      int      `json:"scanId"`
	PathsToRead []string `json:"pathsToRead"`
}

type commitScanPayload struct {
	ScanID int `json:"scanId"`
}

// Handle dispatches a single named RPC method with JSON arguments and returns a serializable response.
func (s *BridgeService) Handle(ctx context.Context, method, payloadJSON string) (any, error) {
	switch method {
	case "configure":
		var req settings.ConfigureRequest
		if err := json.Unmarshal([]byte(payloadJSON), &req); err != nil {
			return nil, fmt.Errorf("invalid configure payload: %w", err)
		}
		return s.configure(req), nil

	case "listFiles":
		var req settings.ListFilesRequest
		if err := json.Unmarshal([]byte(payloadJSON), &req); err != nil {
			return nil, fmt.Errorf("invalid listFiles payload: %w", err)
		}
		return s.listFiles(ctx, req)

	case "readFile":
		var req settings.ReadFileRequest
		if err := json.Unmarshal([]byte(payloadJSON), &req); err != nil {
			return nil, fmt.Errorf("invalid readFile payload: %w", err)
		}
		return s.readFile(ctx, req)

	case "writeFile":
		var req settings.WriteFileRequest
		if err := json.Unmarshal([]byte(payloadJSON), &req); err != nil {
			return nil, fmt.Errorf("invalid writeFile payload: %w", err)
		}
		return s.writeFile(ctx, req)

	case "mergeAndPushFile":
		var req settings.MergeAndPushRequest
		if err := json.Unmarshal([]byte(payloadJSON), &req); err != nil {
			return nil, fmt.Errorf("invalid mergeAndPushFile payload: %w", err)
		}
		return s.mergeAndPushFile(ctx, req)

	case "pullPrepare":
		var req settings.PullPrepareRequest
		if err := json.Unmarshal([]byte(payloadJSON), &req); err != nil {
			return nil, fmt.Errorf("invalid pullPrepare payload: %w", err)
		}
		return s.pullPrepare(ctx, req)

	case "executePreparedPush":
		var req settings.PushExecuteRequest
		if err := json.Unmarshal([]byte(payloadJSON), &req); err != nil {
			return nil, fmt.Errorf("invalid executePreparedPush payload: %w", err)
		}
		return s.executePreparedPush(ctx, req)

	case "recordVaultEvent":
		var hint settings.VaultEventHint
		if err := json.Unmarshal([]byte(payloadJSON), &hint); err != nil {
			return nil, fmt.Errorf("invalid recordVaultEvent payload: %w", err)
		}
		if err := syncstore.RecordVaultEvent(ctx, s.store, hint, s.now()); err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, nil

	case "prepareVaultScan":
		var req prepareScanPayload
		if err := json.Unmarshal([]byte(payloadJSON), &req); err != nil {
			return nil, fmt.Errorf("invalid prepareVaultScan payload: %w", err)
		}
		return s.prepareVaultScan(ctx, req)

	case "commitSyncScan":
		var req commitScanPayload
		if err := json.Unmarshal([]byte(payloadJSON), &req); err != nil {
			return nil, fmt.Errorf("invalid commitSyncScan payload: %w", err)
		}
		return s.commitSyncScan(ctx, req.ScanID)

	case "getBaseSnapshot":
		var req settings.BaseSnapshotRequest
		if err := json.Unmarshal([]byte(payloadJSON), &req); err != nil {
			return nil, fmt.Errorf("invalid getBaseSnapshot payload: %w", err)
		}
		return s.getBaseSnapshot(ctx, req.FileName)

	case "setBaseSnapshot":
		var req settings.SetBaseSnapshotRequest
		if err := json.Unmarshal([]byte(payloadJSON), &req); err != nil {
			return nil, fmt.Errorf("invalid setBaseSnapshot payload: %w", err)
		}
		return s.setBaseSnapshot(ctx, req)

	case "clearBaseSnapshot":
		var req settings.BaseSnapshotRequest
		if err := json.Unmarshal([]byte(payloadJSON), &req); err != nil {
			return nil, fmt.Errorf("invalid clearBaseSnapshot payload: %w", err)
		}
		return s.clearBaseSnapshot(ctx, req.FileName)

	case "runIdbBenchmark":
		var req settings.IDBBenchRequest
		if err := json.Unmarshal([]byte(payloadJSON), &req); err != nil {
			return nil, fmt.Errorf("invalid runIdbBenchmark payload: %w", err)
		}
		return s.runIDBBenchmark(ctx, req.Count)

	default:
		return nil, fmt.Errorf("unknown Go Wasm method %q", method)
	}
}

func (s *BridgeService) configure(req settings.ConfigureRequest) settings.ConfigureResult {
	limit := settings.ClampMaxPendingRequests(req.MaxPendingRequests)
	s.driveTransport.SetMaxPending(limit)
	s.auth.SetRefreshToken(req.RefreshToken)
	if req.VaultName != "" && s.onVaultName != nil {
		s.onVaultName(req.VaultName)
	}
	return settings.ConfigureResult{
		Configured:         req.RefreshToken != "",
		MaxPendingRequests: limit,
	}
}

func (s *BridgeService) listFiles(ctx context.Context, req settings.ListFilesRequest) (settings.ListFilesResult, error) {
	folderID, err := s.client.EnsureFolderPath(ctx, req.FolderPath)
	if err != nil {
		return settings.ListFilesResult{}, err
	}
	files, err := s.client.ListFiles(ctx, folderID)
	if err != nil {
		return settings.ListFilesResult{}, err
	}
	return settings.ListFilesResult{
		FolderID:            folderID,
		Files:               files,
		RotatedRefreshToken: s.auth.TakeRotatedRefreshToken(),
	}, nil
}

func (s *BridgeService) readFile(ctx context.Context, req settings.ReadFileRequest) (settings.ReadFileResult, error) {
	meta, err := s.client.GetFile(ctx, req.FileID)
	if err != nil {
		return settings.ReadFileResult{}, err
	}
	content, err := s.client.ReadFile(ctx, req.FileID)
	if err != nil {
		return settings.ReadFileResult{}, err
	}
	return settings.ReadFileResult{
		File:                meta,
		Content:             content,
		RotatedRefreshToken: s.auth.TakeRotatedRefreshToken(),
	}, nil
}

func (s *BridgeService) writeFile(ctx context.Context, req settings.WriteFileRequest) (settings.WriteFileResult, error) {
	var meta settings.FileMeta
	var err error
	if req.FileID != "" {
		meta, err = s.client.UpdateFile(ctx, req.FileID, req.FileName, req.Content)
	} else {
		folderID, folderErr := s.client.EnsureFolderPath(ctx, req.FolderPath)
		if folderErr != nil {
			return settings.WriteFileResult{}, folderErr
		}
		existing, findErr := s.client.FindFile(ctx, folderID, req.FileName)
		if findErr != nil {
			return settings.WriteFileResult{}, findErr
		}
		if existing != nil {
			meta, err = s.client.UpdateFile(ctx, existing.ID, req.FileName, req.Content)
		} else {
			meta, err = s.client.CreateFile(ctx, folderID, req.FileName, req.Content)
		}
	}
	if err != nil {
		return settings.WriteFileResult{}, err
	}
	return settings.WriteFileResult{
		File:                meta,
		RotatedRefreshToken: s.auth.TakeRotatedRefreshToken(),
	}, nil
}

func (s *BridgeService) mergeAndPushFile(ctx context.Context, req settings.MergeAndPushRequest) (settings.MergeAndPushResult, error) {
	folderID, err := s.client.EnsureFolderPath(ctx, req.FolderPath)
	if err != nil {
		return settings.MergeAndPushResult{}, err
	}
	existing, err := s.client.FindFile(ctx, folderID, req.FileName)
	if err != nil {
		return settings.MergeAndPushResult{}, err
	}

	if existing == nil {
		created, createErr := s.client.CreateFile(ctx, folderID, req.FileName, req.LocalContent)
		if createErr != nil {
			return settings.MergeAndPushResult{}, createErr
		}
		return settings.MergeAndPushResult{
			Outcome:             "clean",
			Content:             req.LocalContent,
			RemoteContent:       "",
			RemoteExisted:       false,
			DriveUpdated:        true,
			LocalUpdated:        false,
			File:                created,
			RotatedRefreshToken: s.auth.TakeRotatedRefreshToken(),
		}, nil
	}

	remoteContent, err := s.client.ReadFile(ctx, existing.ID)
	if err != nil {
		return settings.MergeAndPushResult{}, err
	}

	if remoteContent == req.LocalContent {
		return settings.MergeAndPushResult{
			Outcome:             "noop",
			Content:             req.LocalContent,
			RemoteContent:       remoteContent,
			RemoteExisted:       true,
			DriveUpdated:        false,
			LocalUpdated:        false,
			File:                *existing,
			RotatedRefreshToken: s.auth.TakeRotatedRefreshToken(),
		}, nil
	}

	if !req.HasBase {
		return settings.MergeAndPushResult{
			Outcome:             "keepBoth",
			Content:             "",
			RemoteContent:       remoteContent,
			RemoteExisted:       true,
			DriveUpdated:        false,
			LocalUpdated:        false,
			File:                *existing,
			RotatedRefreshToken: s.auth.TakeRotatedRefreshToken(),
		}, nil
	}

	reconciled := merge.ReconcileText(req.BaseContent, req.LocalContent, remoteContent)
	if reconciled.Kind == "keepBoth" {
		return settings.MergeAndPushResult{
			Outcome:             "keepBoth",
			Content:             "",
			RemoteContent:       remoteContent,
			RemoteExisted:       true,
			DriveUpdated:        false,
			LocalUpdated:        false,
			File:                *existing,
			RotatedRefreshToken: s.auth.TakeRotatedRefreshToken(),
		}, nil
	}

	localUpdated := reconciled.Content != req.LocalContent
	driveUpdated := reconciled.Content != remoteContent
	outMeta := *existing

	if driveUpdated {
		updatedMeta, updateErr := s.client.UpdateFile(ctx, existing.ID, req.FileName, reconciled.Content)
		if updateErr != nil {
			return settings.MergeAndPushResult{}, updateErr
		}
		outMeta = updatedMeta
	}

	outcome := reconciled.Kind
	if !localUpdated && !driveUpdated {
		outcome = "noop"
	}

	return settings.MergeAndPushResult{
		Outcome:             outcome,
		Content:             reconciled.Content,
		RemoteContent:       remoteContent,
		RemoteExisted:       true,
		DriveUpdated:        driveUpdated,
		LocalUpdated:        localUpdated,
		File:                outMeta,
		RotatedRefreshToken: s.auth.TakeRotatedRefreshToken(),
	}, nil
}

// pullPrepare reconciles Drive metadata against IndexedDB (resolving Drive renames),
// downloads eligible files concurrently, and updates IndexedDB records + missing bases.
func (s *BridgeService) pullPrepare(ctx context.Context, req settings.PullPrepareRequest) (settings.PullPrepareResult, error) {
	now := s.now()
	nowStr := now.UTC().Format(time.RFC3339)

	folderID, err := s.client.EnsureFolderPath(ctx, req.FolderPath)
	if err != nil {
		return settings.PullPrepareResult{}, err
	}
	files, err := s.client.ListFiles(ctx, folderID)
	if err != nil {
		return settings.PullPrepareResult{}, err
	}

	if _, recErr := syncstore.ReconcileDriveScan(ctx, s.store, files, now); recErr != nil {
		return settings.PullPrepareResult{}, recErr
	}

	pulled := make([]syncplan.PulledFile, len(files))
	errs := make([]error, len(files))
	var wg sync.WaitGroup
	for i, f := range files {
		wg.Add(1)
		go func(idx int, meta settings.FileMeta) {
			defer wg.Done()
			content, readErr := s.client.ReadFile(ctx, meta.ID)
			if readErr != nil {
				errs[idx] = fmt.Errorf("read %s: %w", meta.Name, readErr)
				return
			}
			pulled[idx] = syncplan.PulledFile{Name: meta.Name, Content: content}
		}(i, f)
	}
	wg.Wait()

	for _, readErr := range errs {
		if readErr != nil {
			return settings.PullPrepareResult{}, readErr
		}
	}

	updatedIDB := 0
	for i, f := range files {
		content := pulled[i].Content
		shaHex, md5Hex, sz := syncstore.ComputeChecksums(content)
		dState := syncstore.DriveMetaToState(f)
		if dState.SHA256Checksum == "" {
			dState.SHA256Checksum = shaHex
		}
		if dState.MD5Checksum == "" {
			dState.MD5Checksum = md5Hex
		}

		existingRec, exists, getErr := s.store.GetRecord(ctx, f.Name)
		if getErr != nil {
			return settings.PullPrepareResult{}, getErr
		}
		if exists && existingRec.RenamedFrom != "" {
			pulled[i].RenamedFrom = existingRec.RenamedFrom
		}

		rec := settings.SyncRecord{
			FileName:  f.Name,
			Drive:     dState,
			UpdatedAt: nowStr,
		}
		if exists && existingRec.Vault != nil {
			rec.Vault = existingRec.Vault
			rec.Vault.DeletedAtMs = 0
		} else {
			rec.Vault = &settings.VaultFileState{}
		}
		rec.Vault.SHA256 = shaHex
		rec.Vault.MD5 = md5Hex
		rec.Vault.Size = sz
		rec.RenamedFrom = ""

		updateBase := !exists || !existingRec.HasBase
		var basePtr *string
		if updateBase {
			basePtr = &content
		} else {
			rec.HasBase = existingRec.HasBase
			rec.BaseMeta = existingRec.BaseMeta
		}

		if putErr := s.store.PutRecordWithBase(ctx, rec, updateBase, basePtr); putErr != nil {
			return settings.PullPrepareResult{}, putErr
		}
		updatedIDB++
	}

	s.pendingMu.Lock()
	s.nextBatchID++
	batchID := s.nextBatchID
	s.pendingPulls[batchID] = &preparedPull{
		driveFiles: files,
		pulled:     pulled,
		updatedIDB: updatedIDB,
	}
	s.pendingMu.Unlock()

	return settings.PullPrepareResult{
		BatchID:             batchID,
		PulledCount:         len(pulled),
		RotatedRefreshToken: s.auth.TakeRotatedRefreshToken(),
	}, nil
}

// ApplyPullBatch applies a prepared Drive pull batch to Obsidian's Vault via JSPI VaultFS.
func (s *BridgeService) ApplyPullBatch(vault syncplan.VaultFS, batchID int, syncFolder string) (settings.PullSummary, error) {
	s.pendingMu.Lock()
	prep, ok := s.pendingPulls[batchID]
	if ok {
		delete(s.pendingPulls, batchID)
	}
	s.pendingMu.Unlock()
	if !ok {
		return settings.PullSummary{}, fmt.Errorf("unknown pull batch %d", batchID)
	}

	res, err := syncplan.ApplyPulledFiles(vault, syncFolder, prep.pulled)
	if err != nil {
		return settings.PullSummary{}, err
	}
	return settings.PullSummary{
		Pulled:     res.Pulled,
		UpdatedIDB: prep.updatedIDB,
		Renamed:    res.Renamed,
	}, nil
}

// PreparePushInputs reads the active local note and its Vault stat via JSPI into Go memory.
func (s *BridgeService) PreparePushInputs(vault syncplan.VaultFS, syncFolder, fileName string) (int, error) {
	if !syncplan.IsEligibleFileName(fileName) {
		return 0, fmt.Errorf("ineligible file name %q", fileName)
	}
	vaultPath := syncplan.JoinVaultPath(syncFolder, fileName)
	localContent, found, err := vault.ReadText(vaultPath)
	if err != nil {
		return 0, fmt.Errorf("read local %s: %w", fileName, err)
	}
	if !found {
		return 0, fmt.Errorf("local file not found: %s", vaultPath)
	}
	var mtimeMs int64
	if stats, stErr := vault.ListFlatStats(syncFolder); stErr == nil {
		for _, st := range stats {
			if st.Name == fileName {
				mtimeMs = st.MTimeMs
				break
			}
		}
	}

	s.pendingMu.Lock()
	s.nextBatchID++
	batchID := s.nextBatchID
	s.pendingPushes[batchID] = &preparedPush{
		fileName:     fileName,
		syncFolder:   syncFolder,
		vaultPath:    vaultPath,
		localContent: localContent,
		mtimeMs:      mtimeMs,
	}
	s.pendingMu.Unlock()
	return batchID, nil
}

func (s *BridgeService) executePreparedPush(ctx context.Context, req settings.PushExecuteRequest) (settings.PushExecuteResult, error) {
	s.pendingMu.Lock()
	prep, ok := s.pendingPushes[req.BatchID]
	s.pendingMu.Unlock()
	if !ok {
		return settings.PushExecuteResult{}, fmt.Errorf("unknown push batch %d", req.BatchID)
	}

	cleanupOnErr := func() {
		s.pendingMu.Lock()
		delete(s.pendingPushes, req.BatchID)
		s.pendingMu.Unlock()
	}

	rec, recFound, err := s.store.GetRecord(ctx, prep.fileName)
	if err != nil {
		cleanupOnErr()
		return settings.PushExecuteResult{}, err
	}

	// Propagate clean local rename on Drive before reconciling content.
	if recFound && rec.Relation == syncstore.RelationLocalRenamed && rec.Drive != nil && rec.Drive.FileID != "" {
		renamedMeta, renameErr := s.client.RenameFile(ctx, rec.Drive.FileID, prep.fileName)
		if renameErr != nil {
			cleanupOnErr()
			return settings.PushExecuteResult{}, renameErr
		}
		rec.Drive = syncstore.DriveMetaToState(renamedMeta)
		rec.RenamedFrom = ""
	}

	var baseContent string
	var hasBase bool
	if recFound && rec.HasBase {
		var baseErr error
		baseContent, _, hasBase, baseErr = s.store.GetBase(ctx, prep.fileName)
		if baseErr != nil {
			cleanupOnErr()
			return settings.PushExecuteResult{}, baseErr
		}
	}

	res, err := s.mergeAndPushFile(ctx, settings.MergeAndPushRequest{
		FolderPath:   req.FolderPath,
		FileName:     prep.fileName,
		BaseContent:  baseContent,
		LocalContent: prep.localContent,
		HasBase:      hasBase,
	})
	if err != nil {
		cleanupOnErr()
		return settings.PushExecuteResult{}, err
	}

	if err := s.persistPushOutcome(ctx, prep, rec, recFound, hasBase, baseContent, res); err != nil {
		cleanupOnErr()
		return settings.PushExecuteResult{}, err
	}

	s.pendingMu.Lock()
	prep.res = res
	prep.executed = true
	s.pendingMu.Unlock()

	return settings.PushExecuteResult{
		Outcome:             res.Outcome,
		File:                res.File,
		RotatedRefreshToken: res.RotatedRefreshToken,
	}, nil
}

func (s *BridgeService) persistPushOutcome(
	ctx context.Context,
	prep *preparedPush,
	existing *settings.SyncRecord,
	existingFound bool,
	hasBase bool,
	_ string,
	res settings.MergeAndPushResult,
) error {
	nowStr := s.now().UTC().Format(time.RFC3339)

	finalLocalText := prep.localContent
	if res.LocalUpdated {
		finalLocalText = res.Content
	}
	localSHA, localMD5, localSz := syncstore.ComputeChecksums(finalLocalText)

	dState := syncstore.DriveMetaToState(res.File)
	if res.Outcome == "clean" || res.Outcome == "noop" || res.Outcome == "folded" {
		cSHA, cMD5, cSz := syncstore.ComputeChecksums(res.Content)
		if dState.SHA256Checksum == "" {
			dState.SHA256Checksum = cSHA
		}
		if dState.MD5Checksum == "" {
			dState.MD5Checksum = cMD5
		}
		if dState.Size == 0 {
			dState.Size = cSz
		}
	}

	// Clear mtimeMs if local file will be overwritten in JSPI apply so next stat scan refreshes real disk mtime.
	recordedMTime := prep.mtimeMs
	if res.LocalUpdated {
		recordedMTime = 0
	}

	nextRec := settings.SyncRecord{
		FileName: prep.fileName,
		Drive:    dState,
		Vault: &settings.VaultFileState{
			Path:    prep.vaultPath,
			Size:    localSz,
			MTimeMs: recordedMTime,
			SHA256:  localSHA,
			MD5:     localMD5,
		},
		UpdatedAt: nowStr,
	}
	if existingFound {
		nextRec.HasBase = existing.HasBase
		nextRec.BaseMeta = existing.BaseMeta
	}

	updateBase := false
	var basePtr *string
	switch res.Outcome {
	case "clean", "noop":
		baseText := finalLocalText
		updateBase = true
		basePtr = &baseText
	case "folded":
		// Preserve existing base so conflict resolution remains anchored against the common ancestor.
		if !hasBase && res.RemoteExisted {
			r := res.RemoteContent
			updateBase = true
			basePtr = &r
		}
	}

	return s.store.PutRecordWithBase(ctx, nextRec, updateBase, basePtr)
}

// ApplyPreparedPush writes any local merged content or keepBoth conflict copy via JSPI VaultFS.
func (s *BridgeService) ApplyPreparedPush(vault syncplan.VaultFS, batchID int) (settings.PushApplySummary, error) {
	s.pendingMu.Lock()
	prep, ok := s.pendingPushes[batchID]
	if ok {
		delete(s.pendingPushes, batchID)
	}
	s.pendingMu.Unlock()
	if !ok || !prep.executed {
		return settings.PushApplySummary{}, fmt.Errorf("push batch %d is not ready", batchID)
	}

	applied, err := syncplan.ApplyPushOutcome(vault, prep.syncFolder, prep.fileName, prep.res)
	if err != nil {
		return settings.PushApplySummary{}, err
	}
	return settings.PushApplySummary{
		Outcome:          prep.res.Outcome,
		ConflictCopyName: applied.ConflictCopyName,
	}, nil
}

// CollectVaultStats gathers flat Vault file stats via JSPI into Go memory for prepareVaultScan.
func (s *BridgeService) CollectVaultStats(vault syncplan.VaultFS, syncFolder string) (int, error) {
	stats, err := vault.ListFlatStats(syncFolder)
	if err != nil {
		return 0, err
	}
	s.pendingMu.Lock()
	s.nextBatchID++
	scanID := s.nextBatchID
	s.pendingScans[scanID] = &preparedScan{
		syncFolder:   syncFolder,
		vaultStats:   stats,
		fileContents: make(map[string]string),
	}
	s.pendingMu.Unlock()
	return scanID, nil
}

func (s *BridgeService) prepareVaultScan(ctx context.Context, req prepareScanPayload) (prepareScanResponse, error) {
	s.pendingMu.Lock()
	scan, ok := s.pendingScans[req.ScanID]
	s.pendingMu.Unlock()
	if !ok {
		return prepareScanResponse{}, fmt.Errorf("unknown scan id %d", req.ScanID)
	}

	existingList, err := s.store.ListRecords(ctx)
	if err != nil {
		return prepareScanResponse{}, err
	}
	byName := make(map[string]settings.SyncRecord, len(existingList))
	for _, r := range existingList {
		byName[r.FileName] = r
	}

	var pathsToRead []string
	for _, st := range scan.vaultStats {
		if !syncplan.IsEligibleFileName(st.Name) {
			continue
		}
		rec, exists := byName[st.Name]
		needsHash := !exists ||
			rec.Vault == nil ||
			rec.Vault.DeletedAtMs > 0 ||
			rec.Vault.SHA256 == "" ||
			rec.Vault.Size != st.Size ||
			rec.Vault.MTimeMs != st.MTimeMs
		if needsHash {
			pathsToRead = append(pathsToRead, st.Path)
		}
	}

	var driveFiles []settings.FileMeta
	var rotatedToken string
	if req.IncludeDrive {
		folderID, folderErr := s.client.EnsureFolderPath(ctx, req.DriveFolderPath)
		if folderErr != nil {
			return prepareScanResponse{}, folderErr
		}
		driveFiles, err = s.client.ListFiles(ctx, folderID)
		if err != nil {
			return prepareScanResponse{}, err
		}
		rotatedToken = s.auth.TakeRotatedRefreshToken()
	}

	s.pendingMu.Lock()
	scan.driveFolder = req.DriveFolderPath
	scan.includeDrive = req.IncludeDrive
	scan.pathsToRead = pathsToRead
	scan.driveFiles = driveFiles
	scan.rotatedToken = rotatedToken
	s.pendingMu.Unlock()

	if pathsToRead == nil {
		pathsToRead = []string{}
	}
	return prepareScanResponse{
		ScanID:      req.ScanID,
		PathsToRead: pathsToRead,
	}, nil
}

// ReadVaultFilesForScan reads only changed/new Vault files into Go memory via JSPI VaultFS.
func (s *BridgeService) ReadVaultFilesForScan(vault syncplan.VaultFS, scanID int) error {
	s.pendingMu.Lock()
	scan, ok := s.pendingScans[scanID]
	s.pendingMu.Unlock()
	if !ok {
		return fmt.Errorf("unknown scan id %d", scanID)
	}

	contents := make(map[string]string, len(scan.pathsToRead))
	for _, p := range scan.pathsToRead {
		text, found, err := vault.ReadText(p)
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		if found {
			contents[p] = text
		}
	}

	s.pendingMu.Lock()
	scan.fileContents = contents
	s.pendingMu.Unlock()
	return nil
}

func (s *BridgeService) commitSyncScan(ctx context.Context, scanID int) (settings.GetSyncStateResult, error) {
	s.pendingMu.Lock()
	scan, ok := s.pendingScans[scanID]
	if ok {
		delete(s.pendingScans, scanID)
	}
	s.pendingMu.Unlock()
	if !ok {
		return settings.GetSyncStateResult{}, fmt.Errorf("unknown scan id %d", scanID)
	}

	now := s.now()
	readCached := func(p string) (string, bool, error) {
		v, exists := scan.fileContents[p]
		return v, exists, nil
	}

	vaultStats, err := syncstore.ReconcileVaultScan(ctx, s.store, scan.vaultStats, readCached, now)
	if err != nil {
		return settings.GetSyncStateResult{}, err
	}

	renamedDrive := 0
	if scan.includeDrive {
		renamedDrive, err = syncstore.ReconcileDriveScan(ctx, s.store, scan.driveFiles, now)
		if err != nil {
			return settings.GetSyncStateResult{}, err
		}
	}

	records, err := s.store.ListRecords(ctx)
	if err != nil {
		return settings.GetSyncStateResult{}, err
	}
	if records == nil {
		records = []settings.SyncRecord{}
	}

	return settings.GetSyncStateResult{
		Records:             records,
		RenamedVaultFiles:   vaultStats.RenamedFiles,
		RenamedDriveFiles:   renamedDrive,
		HashedVaultFiles:    vaultStats.HashedFiles,
		RotatedRefreshToken: scan.rotatedToken,
	}, nil
}

func (s *BridgeService) getBaseSnapshot(ctx context.Context, fileName string) (settings.BaseSnapshotResult, error) {
	content, shaHex, found, err := s.store.GetBase(ctx, fileName)
	if err != nil {
		return settings.BaseSnapshotResult{}, err
	}
	return settings.BaseSnapshotResult{
		FileName: fileName,
		HasBase:  found,
		SHA256:   shaHex,
		Content:  content,
	}, nil
}

func (s *BridgeService) setBaseSnapshot(ctx context.Context, req settings.SetBaseSnapshotRequest) (settings.BaseSnapshotResult, error) {
	if !syncplan.IsEligibleFileName(req.FileName) {
		return settings.BaseSnapshotResult{}, fmt.Errorf("ineligible file name %q", req.FileName)
	}
	nowStr := s.now().UTC().Format(time.RFC3339)
	recPtr, exists, err := s.store.GetRecord(ctx, req.FileName)
	if err != nil {
		return settings.BaseSnapshotResult{}, err
	}
	var rec settings.SyncRecord
	if exists {
		rec = *recPtr
	} else {
		rec = settings.SyncRecord{FileName: req.FileName}
	}
	rec.UpdatedAt = nowStr

	content := req.Content
	if err := s.store.PutRecordWithBase(ctx, rec, true, &content); err != nil {
		return settings.BaseSnapshotResult{}, err
	}
	shaHex, _, _ := syncstore.ComputeChecksums(content)
	return settings.BaseSnapshotResult{
		FileName: req.FileName,
		HasBase:  true,
		SHA256:   shaHex,
		Content:  content,
	}, nil
}

func (s *BridgeService) clearBaseSnapshot(ctx context.Context, fileName string) (settings.BaseSnapshotResult, error) {
	recPtr, exists, err := s.store.GetRecord(ctx, fileName)
	if err != nil {
		return settings.BaseSnapshotResult{}, err
	}
	if exists {
		recPtr.UpdatedAt = s.now().UTC().Format(time.RFC3339)
		if err := s.store.PutRecordWithBase(ctx, *recPtr, true, nil); err != nil {
			return settings.BaseSnapshotResult{}, err
		}
	}
	return settings.BaseSnapshotResult{
		FileName: fileName,
		HasBase:  false,
	}, nil
}

func (s *BridgeService) runIDBBenchmark(ctx context.Context, count int) (settings.IDBBenchResult, error) {
	if count <= 0 {
		count = 100
	}
	if count > 2000 {
		count = 2000
	}

	prefix := "__bench_"
	payload := "IndexedDB benchmark common base payload.\n"

	tWrite := time.Now()
	for i := 0; i < count; i++ {
		name := fmt.Sprintf("%s%04d.md", prefix, i)
		sha, md5Hex, sz := syncstore.ComputeChecksums(payload)
		var basePtr *string
		// Half with null base, half with base content to match realistic mixed workload.
		if i%2 == 0 {
			p := payload
			basePtr = &p
		}
		rec := settings.SyncRecord{
			FileName: name,
			Drive:    &settings.DriveFileState{FileID: fmt.Sprintf("bid-%d", i), Name: name, SHA256Checksum: sha, Size: sz},
			Vault:    &settings.VaultFileState{Path: "bench/" + name, Size: sz, MTimeMs: 100, SHA256: sha, MD5: md5Hex},
		}
		if err := s.store.PutRecordWithBase(ctx, rec, true, basePtr); err != nil {
			return settings.IDBBenchResult{}, err
		}
	}
	writeMs := float64(time.Since(tWrite).Microseconds()) / 1000.0

	tScan := time.Now()
	recs, err := s.store.ListRecords(ctx)
	if err != nil {
		return settings.IDBBenchResult{}, err
	}
	scanMs := float64(time.Since(tScan).Microseconds()) / 1000.0

	tGet := time.Now()
	for i := 0; i < count; i += 2 {
		name := fmt.Sprintf("%s%04d.md", prefix, i)
		if _, _, _, err := s.store.GetBase(ctx, name); err != nil {
			return settings.IDBBenchResult{}, err
		}
	}
	getMs := float64(time.Since(tGet).Microseconds()) / 1000.0

	tClear := time.Now()
	for _, r := range recs {
		if len(r.FileName) >= len(prefix) && r.FileName[:len(prefix)] == prefix {
			if err := s.store.DeleteRecord(ctx, r.FileName); err != nil {
				return settings.IDBBenchResult{}, err
			}
		}
	}
	clearMs := float64(time.Since(tClear).Microseconds()) / 1000.0

	return settings.IDBBenchResult{
		Count:   count,
		WriteMs: writeMs,
		ScanMs:  scanMs,
		GetMs:   getMs,
		ClearMs: clearMs,
	}, nil
}
