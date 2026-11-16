// Package syncstore implements the pure-Go IndexedDB synchronization state model,
// checksum-based change/rename detection, and atomic SyncRecord + BaseSnapshot store contract.
package syncstore

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"test-gdoc/go/settings"
)

// Sync relation constants persisted on settings.SyncRecord.Relation.
const (
	RelationInSync         = "inSync"
	RelationLocalOnly      = "localOnly"
	RelationRemoteOnly     = "remoteOnly"
	RelationLocalModified  = "localModified"
	RelationRemoteModified = "remoteModified"
	RelationBothModified   = "bothModified"
	RelationUnseededBoth   = "unseededBoth"
	RelationLocalDeleted   = "localDeleted"
	RelationLocalRenamed   = "localRenamed"
	RelationRemoteRenamed  = "remoteRenamed"
)

// Store abstracts atomic persistence of compact sync metadata records and optional
// lazy-loaded common base snapshots inside a single per-vault IndexedDB database.
type Store interface {
	GetRecord(ctx context.Context, fileName string) (*settings.SyncRecord, bool, error)
	GetByDriveID(ctx context.Context, driveFileID string) (*settings.SyncRecord, bool, error)
	ListRecords(ctx context.Context) ([]settings.SyncRecord, error)
	PutRecordWithBase(ctx context.Context, rec settings.SyncRecord, updateBase bool, baseContent *string) error
	DeleteRecord(ctx context.Context, fileName string) error
	GetBase(ctx context.Context, fileName string) (content string, sha256Hex string, found bool, err error)
	MigrateKey(ctx context.Context, oldFileName, newFileName string, updated settings.SyncRecord) error
	ClearAll(ctx context.Context) error
}

// ComputeChecksums returns lowercase hex SHA-256, MD5, and byte length of content.
func ComputeChecksums(content string) (sha256Hex, md5Hex string, size int64) {
	b := []byte(content)
	s256 := sha256.Sum256(b)
	m5 := md5.Sum(b)
	return hex.EncodeToString(s256[:]), hex.EncodeToString(m5[:]), int64(len(b))
}

// DriveMetaToState converts a Drive API FileMeta DTO to a persisted DriveFileState.
func DriveMetaToState(m settings.FileMeta) *settings.DriveFileState {
	sz, _ := strconv.ParseInt(strings.TrimSpace(m.Size), 10, 64)
	return &settings.DriveFileState{
		FileID:         m.ID,
		Name:           m.Name,
		MimeType:       m.MimeType,
		ModifiedTime:   m.ModifiedTime,
		Size:           sz,
		SHA256Checksum: strings.ToLower(strings.TrimSpace(m.SHA256Checksum)),
		MD5Checksum:    strings.ToLower(strings.TrimSpace(m.MD5Checksum)),
	}
}

// DriveChecksumMatches returns true if d has a populated SHA-256 or MD5 checksum matching
// the provided content digests.
func DriveChecksumMatches(d *settings.DriveFileState, sha256Hex, md5Hex string) bool {
	if d == nil {
		return false
	}
	if d.SHA256Checksum != "" && sha256Hex != "" {
		return strings.EqualFold(d.SHA256Checksum, sha256Hex)
	}
	if d.MD5Checksum != "" && md5Hex != "" {
		return strings.EqualFold(d.MD5Checksum, md5Hex)
	}
	return false
}

// VaultMatchesBase returns true when Vault digests match stored BaseMeta digests.
func VaultMatchesBase(v *settings.VaultFileState, b *settings.BaseMeta) bool {
	if v == nil || b == nil {
		return false
	}
	if v.SHA256 != "" && b.SHA256 != "" {
		return strings.EqualFold(v.SHA256, b.SHA256)
	}
	if v.MD5 != "" && b.MD5 != "" {
		return strings.EqualFold(v.MD5, b.MD5)
	}
	return false
}

// DriveMatchesBase returns true when Drive digests match stored BaseMeta digests.
func DriveMatchesBase(d *settings.DriveFileState, b *settings.BaseMeta) bool {
	if d == nil || b == nil {
		return false
	}
	return DriveChecksumMatches(d, b.SHA256, b.MD5)
}

// ComputeRelation derives the canonical sync relation from a SyncRecord's Drive, Vault, and Base metadata.
func ComputeRelation(rec *settings.SyncRecord) string {
	hasLiveVault := rec.Vault != nil && rec.Vault.DeletedAtMs == 0
	hasDrive := rec.Drive != nil

	if rec.Vault != nil && rec.Vault.DeletedAtMs > 0 {
		if hasDrive {
			return RelationLocalDeleted
		}
		return RelationLocalOnly
	}

	if hasLiveVault && hasDrive && rec.RenamedFrom != "" {
		if rec.Drive.Name != rec.FileName {
			return RelationLocalRenamed
		}
		vaultBaseName := rec.Vault.Path
		if idx := strings.LastIndexByte(vaultBaseName, '/'); idx >= 0 {
			vaultBaseName = vaultBaseName[idx+1:]
		}
		if vaultBaseName != rec.FileName {
			return RelationRemoteRenamed
		}
	}

	if hasLiveVault && !hasDrive {
		return RelationLocalOnly
	}
	if !hasLiveVault && hasDrive {
		return RelationRemoteOnly
	}
	if !hasLiveVault && !hasDrive {
		return RelationInSync
	}

	if DriveChecksumMatches(rec.Drive, rec.Vault.SHA256, rec.Vault.MD5) {
		return RelationInSync
	}
	if !rec.HasBase || rec.BaseMeta == nil {
		return RelationUnseededBoth
	}

	vaultChanged := !VaultMatchesBase(rec.Vault, rec.BaseMeta)
	driveChanged := !DriveMatchesBase(rec.Drive, rec.BaseMeta)
	switch {
	case vaultChanged && driveChanged:
		return RelationBothModified
	case vaultChanged:
		return RelationLocalModified
	case driveChanged:
		return RelationRemoteModified
	default:
		return RelationInSync
	}
}
