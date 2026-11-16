package syncstore

import (
	"context"
	"fmt"
	"strings"
	"time"

	"test-gdoc/go/settings"
	"test-gdoc/go/syncplan"
)

// VaultScanStats reports work done and renames inferred during a Vault scan.
type VaultScanStats struct {
	HashedFiles  int
	RenamedFiles int
}

// RecordVaultEvent persists lightweight Obsidian create/delete/modify notifications
// without calling vault.on('rename') so offline/live deletes can inform rename matching.
func RecordVaultEvent(ctx context.Context, store Store, hint settings.VaultEventHint, now time.Time) error {
	if !syncplan.IsEligibleFileName(hint.FileName) {
		return nil
	}
	nowStr := now.UTC().Format(time.RFC3339)
	recPtr, ok, err := store.GetRecord(ctx, hint.FileName)
	if err != nil {
		return err
	}
	switch hint.Kind {
	case "delete":
		if !ok {
			return nil
		}
		if recPtr.Drive == nil {
			return store.DeleteRecord(ctx, hint.FileName)
		}
		if recPtr.Vault == nil {
			recPtr.Vault = &settings.VaultFileState{}
		}
		ts := hint.TimestampMs
		if ts <= 0 {
			ts = now.UnixMilli()
		}
		recPtr.Vault.DeletedAtMs = ts
		recPtr.UpdatedAt = nowStr
		recPtr.Relation = ComputeRelation(recPtr)
		return store.PutRecordWithBase(ctx, *recPtr, false, nil)

	case "create", "modify":
		if !ok {
			rec := settings.SyncRecord{
				FileName: hint.FileName,
				Vault: &settings.VaultFileState{
					Size:    hint.Size,
					MTimeMs: hint.MTimeMs,
				},
				UpdatedAt: nowStr,
			}
			rec.Relation = ComputeRelation(&rec)
			return store.PutRecordWithBase(ctx, rec, false, nil)
		}
		if recPtr.Vault == nil {
			recPtr.Vault = &settings.VaultFileState{}
		}
		recPtr.Vault.DeletedAtMs = 0
		recPtr.Vault.Size = hint.Size
		recPtr.Vault.MTimeMs = hint.MTimeMs
		// Clear hashes so the next scan hashes fresh content if mtime/size matched coincidentally.
		if hint.Kind == "modify" {
			recPtr.Vault.SHA256 = ""
			recPtr.Vault.MD5 = ""
		}
		recPtr.UpdatedAt = nowStr
		recPtr.Relation = ComputeRelation(recPtr)
		return store.PutRecordWithBase(ctx, *recPtr, false, nil)

	default:
		return nil
	}
}

// ReconcileVaultScan compares live Obsidian TFile.stat entries against IndexedDB SyncRecords,
// hashing only changed/new files via readText and resolving Vault renames by delete+create
// SHA-256 correlation.
func ReconcileVaultScan(
	ctx context.Context,
	store Store,
	statsList []settings.VaultFileStat,
	readText func(filePath string) (string, bool, error),
	now time.Time,
) (VaultScanStats, error) {
	var scanStats VaultScanStats
	nowStr := now.UTC().Format(time.RFC3339)
	nowMs := now.UnixMilli()

	existingList, err := store.ListRecords(ctx)
	if err != nil {
		return scanStats, err
	}
	byName := make(map[string]settings.SyncRecord, len(existingList))
	for _, r := range existingList {
		byName[r.FileName] = r
	}

	presentByName := make(map[string]settings.VaultFileStat, len(statsList))
	for _, st := range statsList {
		if !syncplan.IsEligibleFileName(st.Name) {
			continue
		}
		presentByName[st.Name] = st
	}

	// Collect vanished local records (previously present in Vault, absent in statsList)
	// whose SHA-256 can match a newly created Vault file (rename without vault.on('rename')).
	vanishedBySHA := make(map[string][]settings.SyncRecord)
	var vanished []settings.SyncRecord
	for _, rec := range existingList {
		if _, present := presentByName[rec.FileName]; present {
			continue
		}
		if rec.Vault == nil {
			continue
		}
		if rec.Drive == nil && rec.Vault.DeletedAtMs == 0 && !rec.HasBase {
			// A purely localOnly file that disappeared with no Drive counterpart.
			vanished = append(vanished, rec)
			continue
		}
		vanished = append(vanished, rec)
		sha := rec.Vault.SHA256
		if sha == "" && rec.BaseMeta != nil {
			sha = rec.BaseMeta.SHA256
		}
		if sha != "" {
			vanishedBySHA[sha] = append(vanishedBySHA[sha], rec)
		}
	}

	consumedOld := make(map[string]bool)

	for _, st := range statsList {
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

		shaHex := ""
		md5Hex := ""
		if needsHash {
			content, found, readErr := readText(st.Path)
			if readErr != nil {
				return scanStats, fmt.Errorf("read vault file %s: %w", st.Path, readErr)
			}
			if !found {
				continue
			}
			shaHex, md5Hex, _ = ComputeChecksums(content)
			scanStats.HashedFiles++
		} else {
			shaHex = rec.Vault.SHA256
			md5Hex = rec.Vault.MD5
		}

		// If this file is newly created in Vault (either unseen or an unhashed create-event placeholder),
		// check whether a vanished/deleted record shares the exact SHA-256 (rename without vault.on('rename')).
		isNewLocal := !exists ||
			rec.Vault == nil ||
			rec.Vault.DeletedAtMs > 0 ||
			(rec.Drive == nil && !rec.HasBase && rec.Vault.SHA256 == "")
		if isNewLocal && shaHex != "" {
			matches := vanishedBySHA[shaHex]
			if len(matches) == 1 && !consumedOld[matches[0].FileName] {
				oldRec := matches[0]
				consumedOld[oldRec.FileName] = true
				migrated := oldRec
				migrated.FileName = st.Name
				migrated.RenamedFrom = oldRec.FileName
				migrated.UpdatedAt = nowStr
				migrated.Vault = &settings.VaultFileState{
					Path:    st.Path,
					Size:    st.Size,
					MTimeMs: st.MTimeMs,
					SHA256:  shaHex,
					MD5:     md5Hex,
				}
				if err := store.MigrateKey(ctx, oldRec.FileName, st.Name, migrated); err != nil {
					return scanStats, err
				}
				scanStats.RenamedFiles++
				continue
			}
		}

		if !needsHash && exists && rec.Vault != nil && rec.Vault.DeletedAtMs == 0 {
			expectedRel := ComputeRelation(&rec)
			if rec.Relation == expectedRel && !(rec.Drive != nil && rec.Drive.Name == st.Name && rec.RenamedFrom != "") {
				continue
			}
		}

		if !exists {
			rec = settings.SyncRecord{FileName: st.Name}
		}
		if rec.Vault == nil {
			rec.Vault = &settings.VaultFileState{}
		}
		rec.Vault.Path = st.Path
		rec.Vault.Size = st.Size
		rec.Vault.MTimeMs = st.MTimeMs
		rec.Vault.SHA256 = shaHex
		rec.Vault.MD5 = md5Hex
		rec.Vault.DeletedAtMs = 0
		if rec.Drive != nil && rec.Drive.Name == st.Name {
			rec.RenamedFrom = ""
		}
		rec.UpdatedAt = nowStr
		rec.Relation = ComputeRelation(&rec)

		if err := store.PutRecordWithBase(ctx, rec, false, nil); err != nil {
			return scanStats, err
		}
	}

	// Finalize vanished records not consumed as renames.
	for _, vRec := range vanished {
		if consumedOld[vRec.FileName] {
			continue
		}
		if vRec.Drive == nil {
			if err := store.DeleteRecord(ctx, vRec.FileName); err != nil {
				return scanStats, err
			}
			continue
		}
		if vRec.Vault.DeletedAtMs == 0 {
			vRec.Vault.DeletedAtMs = nowMs
			vRec.UpdatedAt = nowStr
			vRec.Relation = ComputeRelation(&vRec)
			if err := store.PutRecordWithBase(ctx, vRec, false, nil); err != nil {
				return scanStats, err
			}
		}
	}

	return scanStats, nil
}

// ReconcileDriveScan updates SyncRecords against a Drive folder listing, resolving
// Drive-side renames deterministically via GetByDriveID.
func ReconcileDriveScan(
	ctx context.Context,
	store Store,
	driveFiles []settings.FileMeta,
	now time.Time,
) (int, error) {
	renamed := 0
	nowStr := now.UTC().Format(time.RFC3339)

	existingList, err := store.ListRecords(ctx)
	if err != nil {
		return 0, err
	}
	byName := make(map[string]settings.SyncRecord, len(existingList))
	for _, r := range existingList {
		byName[r.FileName] = r
	}

	seenDriveIDs := make(map[string]bool, len(driveFiles))

	for _, df := range driveFiles {
		if !syncplan.IsEligibleFileName(df.Name) {
			continue
		}
		seenDriveIDs[df.ID] = true
		dState := DriveMetaToState(df)

		if rec, ok := byName[df.Name]; ok {
			rec.Drive = dState
			rec.DriveFileID = df.ID
			rec.UpdatedAt = nowStr
			if rec.Vault != nil && strings.HasSuffix(rec.Vault.Path, "/"+df.Name) {
				rec.RenamedFrom = ""
			}
			rec.Relation = ComputeRelation(&rec)
			if err := store.PutRecordWithBase(ctx, rec, false, nil); err != nil {
				return renamed, err
			}
			byName[df.Name] = rec
			continue
		}

		// Not under df.Name: check if this Drive fileId existed under an old name (Drive rename).
		if prev, foundByID, idErr := store.GetByDriveID(ctx, df.ID); idErr != nil {
			return renamed, idErr
		} else if foundByID && prev.FileName != df.Name {
			oldName := prev.FileName
			prev.Drive = dState
			prev.DriveFileID = df.ID
			prev.RenamedFrom = oldName
			prev.UpdatedAt = nowStr
			if err := store.MigrateKey(ctx, oldName, df.Name, *prev); err != nil {
				return renamed, err
			}
			delete(byName, oldName)
			prev.FileName = df.Name
			prev.Relation = ComputeRelation(prev)
			byName[df.Name] = *prev
			renamed++
			continue
		}

		newRec := settings.SyncRecord{
			FileName:    df.Name,
			DriveFileID: df.ID,
			Drive:       dState,
			UpdatedAt:   nowStr,
		}
		newRec.Relation = ComputeRelation(&newRec)
		if err := store.PutRecordWithBase(ctx, newRec, false, nil); err != nil {
			return renamed, err
		}
		byName[df.Name] = newRec
	}

	// Any record whose DriveFileID disappeared from Drive loses its Drive side (and deletes record if Vault is also absent).
	for _, rec := range existingList {
		if rec.Drive == nil || seenDriveIDs[rec.Drive.FileID] {
			continue
		}
		if _, renamedInto := byName[rec.FileName]; !renamedInto {
			continue
		}
		hasLiveVault := rec.Vault != nil && rec.Vault.DeletedAtMs == 0
		if !hasLiveVault {
			if err := store.DeleteRecord(ctx, rec.FileName); err != nil {
				return renamed, err
			}
			continue
		}
		rec.Drive = nil
		rec.DriveFileID = ""
		rec.UpdatedAt = nowStr
		rec.Relation = ComputeRelation(&rec)
		if err := store.PutRecordWithBase(ctx, rec, false, nil); err != nil {
			return renamed, err
		}
	}

	return renamed, nil
}
