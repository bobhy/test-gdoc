package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"test-gdoc/go/gdrive"
	"test-gdoc/go/merge"
	"test-gdoc/go/settings"
)

// BridgeService routes typed JSON RPC calls from TypeScript to the pure-Go gdrive and merge packages.
type BridgeService struct {
	auth           *gdrive.TokenManager
	client         *gdrive.Client
	driveTransport *gdrive.Transport
}

// NewBridgeService constructs a BridgeService from a transport DoFunc (in Wasm, bridged to requestUrl).
func NewBridgeService(doFunc gdrive.DoFunc, retry gdrive.RetryOptions) *BridgeService {
	authTransport := gdrive.NewTransport(doFunc, 0) // un-throttled so token refresh never contends with Drive slots
	authClient := &http.Client{Transport: authTransport}
	tm := gdrive.NewTokenManager(authClient, gdrive.DefaultRelayBaseURL)

	driveTransport := gdrive.NewTransport(doFunc, settings.DefaultSettings().MaxPendingRequests)
	driveClient := gdrive.NewClient(&http.Client{Transport: driveTransport}, tm, retry)

	return &BridgeService{
		auth:           tm,
		client:         driveClient,
		driveTransport: driveTransport,
	}
}

// Handle dispatches a single named RPC method with JSON arguments and returns a serializable response struct.
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

	default:
		return nil, fmt.Errorf("unknown Go Wasm method %q", method)
	}
}

func (s *BridgeService) configure(req settings.ConfigureRequest) settings.ConfigureResult {
	limit := settings.ClampMaxPendingRequests(req.MaxPendingRequests)
	s.driveTransport.SetMaxPending(limit)
	s.auth.SetRefreshToken(req.RefreshToken)
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
