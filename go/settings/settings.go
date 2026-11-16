// Package settings defines the plugin configuration, IndexedDB sync-state records,
// and RPC data-transfer structs shared between Go and TypeScript as the single source of truth.
package settings

import "strings"

// MinPendingRequests is the lower bound for concurrent outbound Drive HTTP calls.
const MinPendingRequests = 1

// MaxPendingRequestsLimit is the upper bound for concurrent outbound Drive HTTP calls.
const MaxPendingRequestsLimit = 10

// PluginSettings holds the Obsidian plugin settings shared with Go.
// Its JSON tags and default values are the single source of truth for the TypeScript UI.
type PluginSettings struct {
	DriveFolderPath     string `json:"driveFolderPath"`
	LocalSyncFolderPath string `json:"localSyncFolderPath"`
	MaxPendingRequests  int    `json:"maxPendingRequests"`
}

// DefaultSettings returns the canonical default settings for the plugin.
func DefaultSettings() PluginSettings {
	return PluginSettings{
		DriveFolderPath:     "/test-gdoc",
		LocalSyncFolderPath: "test-gdoc-sync",
		MaxPendingRequests:  3,
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
	ID             string `json:"id"`
	Name           string `json:"name"`
	MimeType       string `json:"mimeType"`
	ModifiedTime   string `json:"modifiedTime"`
	Size           string `json:"size"`
	SHA256Checksum string `json:"sha256Checksum"`
	MD5Checksum    string `json:"md5Checksum"`
}

// DriveFileState captures Google Drive metadata stored in IndexedDB for a synced file.
type DriveFileState struct {
	FileID         string `json:"fileId"`
	Name           string `json:"name"`
	MimeType       string `json:"mimeType"`
	ModifiedTime   string `json:"modifiedTime"`
	Size           int64  `json:"size"`
	SHA256Checksum string `json:"sha256Checksum"`
	MD5Checksum    string `json:"md5Checksum"`
}

// VaultFileState captures Obsidian Vault metadata and cached hashes stored in IndexedDB.
type VaultFileState struct {
	Path        string `json:"path"`
	Size        int64  `json:"size"`
	MTimeMs     int64  `json:"mtimeMs"`
	SHA256      string `json:"sha256"`
	MD5         string `json:"md5"`
	DeletedAtMs int64  `json:"deletedAtMs"`
}

// BaseMeta records compact checksum/size metadata for a stored common base snapshot.
// Absent (nil) whenever no common base is stored so null-base rows stay lightweight.
type BaseMeta struct {
	SHA256 string `json:"sha256"`
	MD5    string `json:"md5"`
	Size   int64  `json:"size"`
}

// SyncRecord is the unified IndexedDB metadata record for a flat file, joining
// optional Drive state, optional Vault state, compact base metadata, and sync relation.
type SyncRecord struct {
	FileName    string          `json:"fileName"`
	DriveFileID string          `json:"driveFileId"`
	Drive       *DriveFileState `json:"drive"`
	Vault       *VaultFileState `json:"vault"`
	BaseMeta    *BaseMeta       `json:"baseMeta"`
	HasBase     bool            `json:"hasBase"`
	Relation    string          `json:"relation"`
	RenamedFrom string          `json:"renamedFrom"`
	UpdatedAt   string          `json:"updatedAt"`
}

// VaultFileStat describes lightweight TFile.stat metadata gathered without reading file bytes.
type VaultFileStat struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	MTimeMs int64  `json:"mtimeMs"`
}

// VaultEventHint records a recent Obsidian vault create/delete/modify notification.
type VaultEventHint struct {
	Kind        string `json:"kind"`
	FileName    string `json:"fileName"`
	Size        int64  `json:"size"`
	MTimeMs     int64  `json:"mtimeMs"`
	TimestampMs int64  `json:"timestampMs"`
}

// ConfigureRequest initializes or updates Go-side auth, vault DB scope, and concurrency settings.
type ConfigureRequest struct {
	RefreshToken       string `json:"refreshToken"`
	MaxPendingRequests int    `json:"maxPendingRequests"`
	VaultName          string `json:"vaultName"`
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

// PullPrepareRequest specifies the Drive folder to download in parallel inside Go.
type PullPrepareRequest struct {
	FolderPath string `json:"folderPath"`
}

// PullPrepareResult returns a Go-retained batch handle for JSPI Vault application.
type PullPrepareResult struct {
	BatchID             int    `json:"batchId"`
	PulledCount         int    `json:"pulledCount"`
	RotatedRefreshToken string `json:"rotatedRefreshToken"`
}

// PullSummary reports local files written and IndexedDB sync records updated by a Pull.
type PullSummary struct {
	Pulled     int `json:"pulled"`
	UpdatedIDB int `json:"updatedIdb"`
	Renamed    int `json:"renamed"`
}

// PushExecuteRequest reconciles and pushes a prepared local+base batch against Drive.
type PushExecuteRequest struct {
	FolderPath string `json:"folderPath"`
	BatchID    int    `json:"batchId"`
}

// PushExecuteResult returns lightweight reconciliation status while keeping body text in Go memory.
type PushExecuteResult struct {
	Outcome             string   `json:"outcome"`
	File                FileMeta `json:"file"`
	RotatedRefreshToken string   `json:"rotatedRefreshToken"`
}

// PushApplySummary describes local vault mutations applied by JSPI after Drive reconciliation.
type PushApplySummary struct {
	Outcome          string `json:"outcome"`
	ConflictCopyName string `json:"conflictCopyName"`
}

// GetSyncStateRequest refreshes IndexedDB sync state from Vault stats and optional Drive listing.
type GetSyncStateRequest struct {
	DriveFolderPath     string `json:"driveFolderPath"`
	LocalSyncFolderPath string `json:"localSyncFolderPath"`
	IncludeDrive        bool   `json:"includeDrive"`
}

// GetSyncStateResult returns all IndexedDB SyncRecords and scan metrics.
type GetSyncStateResult struct {
	Records             []SyncRecord `json:"records"`
	RenamedVaultFiles   int          `json:"renamedVaultFiles"`
	RenamedDriveFiles   int          `json:"renamedDriveFiles"`
	HashedVaultFiles    int          `json:"hashedVaultFiles"`
	RotatedRefreshToken string       `json:"rotatedRefreshToken"`
}

// BaseSnapshotRequest identifies a single stored base snapshot by flat filename.
type BaseSnapshotRequest struct {
	FileName string `json:"fileName"`
}

// BaseSnapshotResult returns whether a common base exists in IndexedDB and its content.
type BaseSnapshotResult struct {
	FileName string `json:"fileName"`
	HasBase  bool   `json:"hasBase"`
	SHA256   string `json:"sha256"`
	Content  string `json:"content"`
}

// SetBaseSnapshotRequest writes or updates a stored common base snapshot in IndexedDB.
type SetBaseSnapshotRequest struct {
	FileName string `json:"fileName"`
	Content  string `json:"content"`
}

// IDBBenchRequest specifies how many synthetic records to write, scan, read, and clear.
type IDBBenchRequest struct {
	Count int `json:"count"`
}

// IDBBenchResult measures IndexedDB transaction latencies executed via hack-pad/go-indexeddb.
type IDBBenchResult struct {
	Count   int     `json:"count"`
	WriteMs float64 `json:"writeMs"`
	ScanMs  float64 `json:"scanMs"`
	GetMs   float64 `json:"getMs"`
	ClearMs float64 `json:"clearMs"`
}
