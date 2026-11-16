package syncstore

import (
	"context"
	"testing"
	"time"

	"test-gdoc/go/settings"
)

func TestComputeRelationStates(t *testing.T) {
	shaA, md5A, szA := ComputeChecksums("alpha")
	shaB, md5B, szB := ComputeChecksums("beta")

	cases := []struct {
		name string
		rec  settings.SyncRecord
		want string
	}{
		{
			name: "localOnly with null base",
			rec: settings.SyncRecord{
				FileName: "a.md",
				Vault:    &settings.VaultFileState{Path: "sync/a.md", SHA256: shaA, MD5: md5A, Size: szA},
			},
			want: RelationLocalOnly,
		},
		{
			name: "remoteOnly with null base",
			rec: settings.SyncRecord{
				FileName: "a.md",
				Drive:    &settings.DriveFileState{FileID: "f1", Name: "a.md", SHA256Checksum: shaA, MD5Checksum: md5A},
			},
			want: RelationRemoteOnly,
		},
		{
			name: "inSync matching sha256 even with null base",
			rec: settings.SyncRecord{
				FileName: "a.md",
				Vault:    &settings.VaultFileState{Path: "sync/a.md", SHA256: shaA, MD5: md5A, Size: szA},
				Drive:    &settings.DriveFileState{FileID: "f1", Name: "a.md", SHA256Checksum: shaA},
			},
			want: RelationInSync,
		},
		{
			name: "unseededBoth when differing and base is null",
			rec: settings.SyncRecord{
				FileName: "a.md",
				Vault:    &settings.VaultFileState{Path: "sync/a.md", SHA256: shaA, MD5: md5A, Size: szA},
				Drive:    &settings.DriveFileState{FileID: "f1", Name: "a.md", SHA256Checksum: shaB},
			},
			want: RelationUnseededBoth,
		},
		{
			name: "localModified",
			rec: settings.SyncRecord{
				FileName: "a.md",
				HasBase:  true,
				BaseMeta: &settings.BaseMeta{SHA256: shaA, MD5: md5A, Size: szA},
				Vault:    &settings.VaultFileState{Path: "sync/a.md", SHA256: shaB, MD5: md5B, Size: szB},
				Drive:    &settings.DriveFileState{FileID: "f1", Name: "a.md", SHA256Checksum: shaA},
			},
			want: RelationLocalModified,
		},
		{
			name: "bothModified",
			rec: settings.SyncRecord{
				FileName: "a.md",
				HasBase:  true,
				BaseMeta: &settings.BaseMeta{SHA256: shaA, MD5: md5A, Size: szA},
				Vault:    &settings.VaultFileState{Path: "sync/a.md", SHA256: shaB, MD5: md5B, Size: szB},
				Drive:    &settings.DriveFileState{FileID: "f1", Name: "a.md", SHA256Checksum: "0000"},
			},
			want: RelationBothModified,
		},
		{
			name: "localDeleted with drive counterpart",
			rec: settings.SyncRecord{
				FileName: "a.md",
				Vault:    &settings.VaultFileState{Path: "sync/a.md", DeletedAtMs: 1234},
				Drive:    &settings.DriveFileState{FileID: "f1", Name: "a.md", SHA256Checksum: shaA},
			},
			want: RelationLocalDeleted,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ComputeRelation(&tc.rec); got != tc.want {
				t.Fatalf("ComputeRelation() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestVaultStatCacheAndDeleteCreateRenameDetection(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	now := time.Unix(1700000000, 0)

	vaultContent := map[string]string{"sync/note.md": "hello renameable"}
	readFn := func(p string) (string, bool, error) {
		v, ok := vaultContent[p]
		return v, ok, nil
	}

	baseText := "hello renameable"
	sha, md5Hex, sz := ComputeChecksums(baseText)
	err := store.PutRecordWithBase(ctx, settings.SyncRecord{
		FileName: "note.md",
		Drive: &settings.DriveFileState{
			FileID:         "f-1",
			Name:           "note.md",
			SHA256Checksum: sha,
			MD5Checksum:    md5Hex,
			Size:           sz,
		},
		Vault: &settings.VaultFileState{
			Path:    "sync/note.md",
			Size:    sz,
			MTimeMs: 100,
			SHA256:  sha,
			MD5:     md5Hex,
		},
	}, true, &baseText)
	if err != nil {
		t.Fatalf("seed record: %v", err)
	}

	// Scan 1: unchanged (mtimeMs, size) must skip reading/hashing.
	s1, err := ReconcileVaultScan(ctx, store, []settings.VaultFileStat{
		{Name: "note.md", Path: "sync/note.md", Size: sz, MTimeMs: 100},
	}, readFn, now)
	if err != nil || s1.HashedFiles != 0 {
		t.Fatalf("expected 0 hashed files on stat hit, got %+v err=%v", s1, err)
	}

	// Vault emits delete(note.md) + create(renamed.md) with identical content.
	if err := RecordVaultEvent(ctx, store, settings.VaultEventHint{Kind: "delete", FileName: "note.md"}, now); err != nil {
		t.Fatalf("delete hint: %v", err)
	}
	if err := RecordVaultEvent(ctx, store, settings.VaultEventHint{Kind: "create", FileName: "renamed.md", Size: sz, MTimeMs: 200}, now); err != nil {
		t.Fatalf("create hint: %v", err)
	}
	delete(vaultContent, "sync/note.md")
	vaultContent["sync/renamed.md"] = "hello renameable"

	s2, err := ReconcileVaultScan(ctx, store, []settings.VaultFileStat{
		{Name: "renamed.md", Path: "sync/renamed.md", Size: sz, MTimeMs: 200},
	}, readFn, now.Add(time.Second))
	if err != nil {
		t.Fatalf("ReconcileVaultScan rename: %v", err)
	}
	if s2.RenamedFiles != 1 || s2.HashedFiles != 1 {
		t.Fatalf("unexpected scan stats on rename: %+v", s2)
	}

	// Old key and base must be gone; new key must retain Drive fileId, base, and localRenamed relation.
	if _, ok, _ := store.GetRecord(ctx, "note.md"); ok {
		t.Fatal("old record note.md still exists after MigrateKey")
	}
	if _, _, hasOldBase, _ := store.GetBase(ctx, "note.md"); hasOldBase {
		t.Fatal("old base note.md still exists after MigrateKey")
	}
	rec, ok, err := store.GetRecord(ctx, "renamed.md")
	if err != nil || !ok {
		t.Fatalf("expected renamed.md record, ok=%v err=%v", ok, err)
	}
	if rec.Relation != RelationLocalRenamed || rec.RenamedFrom != "note.md" || rec.DriveFileID != "f-1" || !rec.HasBase {
		t.Fatalf("unexpected migrated record: %+v", rec)
	}
	gotBase, _, hasBase, err := store.GetBase(ctx, "renamed.md")
	if err != nil || !hasBase || gotBase != "hello renameable" {
		t.Fatalf("migrated base mismatch: hasBase=%v content=%q err=%v", hasBase, gotBase, err)
	}
}

func TestDriveRenameDetectionAndNullBaseIntegrity(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	now := time.Unix(1700000000, 0)

	baseText := "shared base"
	sha, md5Hex, sz := ComputeChecksums(baseText)
	if err := store.PutRecordWithBase(ctx, settings.SyncRecord{
		FileName: "orig.md",
		Drive:    &settings.DriveFileState{FileID: "f-9", Name: "orig.md", SHA256Checksum: sha},
		Vault:    &settings.VaultFileState{Path: "sync/orig.md", Size: sz, MTimeMs: 100, SHA256: sha, MD5: md5Hex},
	}, true, &baseText); err != nil {
		t.Fatalf("PutRecordWithBase: %v", err)
	}

	// Clearing base removes base from bases store atomically while keeping SyncRecord.
	r, _, _ := store.GetRecord(ctx, "orig.md")
	if err := store.PutRecordWithBase(ctx, *r, true, nil); err != nil {
		t.Fatalf("clear base: %v", err)
	}
	if _, _, hasBase, _ := store.GetBase(ctx, "orig.md"); hasBase {
		t.Fatal("expected base to be deleted when nil")
	}

	// Put base back and test Drive-side rename detection via driveFileId.
	if err := store.PutRecordWithBase(ctx, *r, true, &baseText); err != nil {
		t.Fatalf("re-seed base: %v", err)
	}
	renamed, err := ReconcileDriveScan(ctx, store, []settings.FileMeta{
		{ID: "f-9", Name: "drive-new.md", MimeType: "text/markdown", SHA256Checksum: sha},
	}, now)
	if err != nil {
		t.Fatalf("ReconcileDriveScan: %v", err)
	}
	if renamed != 1 {
		t.Fatalf("renamed = %d, want 1", renamed)
	}
	migrated, ok, _ := store.GetRecord(ctx, "drive-new.md")
	if !ok || migrated.Relation != RelationRemoteRenamed || migrated.RenamedFrom != "orig.md" || !migrated.HasBase {
		t.Fatalf("unexpected Drive-renamed record: %+v", migrated)
	}
}
