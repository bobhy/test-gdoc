// Package settings defines the plugin configuration and RPC data-transfer
// structs shared between Go and TypeScript as the single source of truth.
package settings

import "strings"

// MinPendingRequests is the lower bound for concurrent outbound Drive HTTP calls.
const MinPendingRequests = 1

// MaxPendingRequestsLimit is the upper bound for concurrent outbound Drive HTTP calls.
const MaxPendingRequestsLimit = 10

// PluginSettings holds the Obsidian plugin settings shared with Go.
// Its JSON tags and default values are the single source of truth for the TypeScript UI.
type PluginSettings struct {
	DriveFolderPath        string `json:"driveFolderPath"`
	LocalSyncFolderPath    string `json:"localSyncFolderPath"`
	BaseSnapshotFolderPath string `json:"baseSnapshotFolderPath"`
	MaxPendingRequests     int    `json:"maxPendingRequests"`
}

// DefaultSettings returns the canonical default settings for the plugin.
func DefaultSettings() PluginSettings {
	return PluginSettings{
		DriveFolderPath:        "/test-gdoc",
		LocalSyncFolderPath:    "test-gdoc-sync",
		BaseSnapshotFolderPath: "test-gdoc-base",
		MaxPendingRequests:     3,
	}
}

// NormalizeSettings applies defaults for empty paths and clamps MaxPendingRequests to [1, 10].
func NormalizeSettings(cfg PluginSettings) PluginSettings {
	def := DefaultSettings()
	if strings.TrimSpace(cfg.DriveFolderPath) == "" {
		cfg.DriveFolderPath = def.DriveFolderPath
	}
	if strings.TrimSpace(cfg.LocalSyncFolderPath) == "" {
		cfg.LocalSyncFolderPath = def.LocalSyncFolderPath
	}
	if strings.TrimSpace(cfg.BaseSnapshotFolderPath) == "" {
		cfg.BaseSnapshotFolderPath = def.BaseSnapshotFolderPath
	}
	cfg.MaxPendingRequests = ClampMaxPendingRequests(cfg.MaxPendingRequests)
	return cfg
}

// ClampMaxPendingRequests ensures n falls within [1, 10], defaulting non-positive values to 3.
func ClampMaxPendingRequests(n int) int {
	if n <= 0 {
		return DefaultSettings().MaxPendingRequests
	}
	if n > MaxPendingRequestsLimit {
		return MaxPendingRequestsLimit
	}
	return n
}

// FileMeta describes a Google Drive file resource exposed across the Wasm bridge.
type FileMeta struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	MimeType     string `json:"mimeType"`
	ModifiedTime string `json:"modifiedTime"`
	Size         string `json:"size"`
}

// ConfigureRequest initializes or updates Go-side auth and concurrency configuration.
type ConfigureRequest struct {
	RefreshToken       string `json:"refreshToken"`
	MaxPendingRequests int    `json:"maxPendingRequests"`
}

// ConfigureResult reports the active Go-side configuration state.
type ConfigureResult struct {
	Configured         bool `json:"configured"`
	MaxPendingRequests int  `json:"maxPendingRequests"`
}

// ListFilesRequest specifies the target Drive folder path to list.
type ListFilesRequest struct {
	FolderPath string `json:"folderPath"`
}

// ListFilesResult returns the resolved folder ID and flat eligible Drive files.
type ListFilesResult struct {
	FolderID            string     `json:"folderId"`
	Files               []FileMeta `json:"files"`
	RotatedRefreshToken string     `json:"rotatedRefreshToken"`
}

// ReadFileRequest identifies a Drive file to download by ID.
type ReadFileRequest struct {
	FileID string `json:"fileId"`
}

// ReadFileResult returns file metadata and UTF-8 text content downloaded from Drive.
type ReadFileResult struct {
	File                FileMeta `json:"file"`
	Content             string   `json:"content"`
	RotatedRefreshToken string   `json:"rotatedRefreshToken"`
}

// WriteFileRequest writes content to an existing Drive file ID or creates a named file in FolderPath.
type WriteFileRequest struct {
	FolderPath string `json:"folderPath"`
	FileID     string `json:"fileId"`
	FileName   string `json:"fileName"`
	Content    string `json:"content"`
}

// WriteFileResult returns the updated or created Drive file metadata.
type WriteFileResult struct {
	File                FileMeta `json:"file"`
	RotatedRefreshToken string   `json:"rotatedRefreshToken"`
}

// MergeAndPushRequest supplies local note content and optional base snapshot content for reconciliation.
type MergeAndPushRequest struct {
	FolderPath   string `json:"folderPath"`
	FileName     string `json:"fileName"`
	BaseContent  string `json:"baseContent"`
	LocalContent string `json:"localContent"`
	HasBase      bool   `json:"hasBase"`
}

// MergeAndPushResult describes the outcome ("clean", "folded", "keepBoth", "noop") of 3-way reconciliation.
type MergeAndPushResult struct {
	Outcome             string   `json:"outcome"`
	Content             string   `json:"content"`
	RemoteContent       string   `json:"remoteContent"`
	RemoteExisted       bool     `json:"remoteExisted"`
	DriveUpdated        bool     `json:"driveUpdated"`
	LocalUpdated        bool     `json:"localUpdated"`
	File                FileMeta `json:"file"`
	RotatedRefreshToken string   `json:"rotatedRefreshToken"`
}
