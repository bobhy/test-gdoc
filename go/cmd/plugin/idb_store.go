//go:build js && wasm

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"syscall/js"

	"github.com/hack-pad/go-indexeddb/idb"

	"test-gdoc/go/settings"
	"test-gdoc/go/syncstore"
)

const (
	idbSchemaVersion = 1
	storeSyncRecords = "sync_records"
	storeBases       = "bases"
	indexDriveFileID = "byDriveFileId"
)

// idbStore implements syncstore.Store directly over browser window.indexedDB
// using github.com/hack-pad/go-indexeddb/idb.
type idbStore struct {
	mu      sync.Mutex
	dbName  string
	db      *idb.Database
	jsonVal js.Value
	objCtor js.Value
}

func newIDBStore(vaultName string) *idbStore {
	return &idbStore{
		dbName:  dbNameForVault(vaultName),
		jsonVal: js.Global().Get("JSON"),
		objCtor: js.Global().Get("Object"),
	}
}

func dbNameForVault(vaultName string) string {
	trimmed := strings.ToLower(strings.TrimSpace(vaultName))
	var b strings.Builder
	lastDash := false
	for _, r := range trimmed {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
		} else if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		slug = "default"
	}
	return "test-gdoc-" + slug
}

func (s *idbStore) SetVaultName(vaultName string) {
	next := dbNameForVault(vaultName)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dbName == next {
		return
	}
	if s.db != nil {
		_ = s.db.Close()
		s.db = nil
	}
	s.dbName = next
}

func (s *idbStore) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		_ = s.db.Close()
		s.db = nil
	}
}

func (s *idbStore) openDB(ctx context.Context) (*idb.Database, error) {
	s.mu.Lock()
	if s.db != nil {
		db := s.db
		s.mu.Unlock()
		return db, nil
	}
	dbName := s.dbName
	s.mu.Unlock()

	factory := idb.Global()
	if factory == nil {
		return nil, fmt.Errorf("window.indexedDB is unavailable")
	}

	req, err := factory.Open(ctx, dbName, idbSchemaVersion, func(db *idb.Database, _ uint, _ uint) error {
		names, namesErr := db.ObjectStoreNames()
		if namesErr != nil {
			return namesErr
		}
		hasRecords := false
		hasBases := false
		for _, n := range names {
			if n == storeSyncRecords {
				hasRecords = true
			} else if n == storeBases {
				hasBases = true
			}
		}
		if !hasRecords {
			recStore, createErr := db.CreateObjectStore(storeSyncRecords, idb.ObjectStoreOptions{
				KeyPath: js.ValueOf("fileName"),
			})
			if createErr != nil {
				return createErr
			}
			if _, idxErr := recStore.CreateIndex(indexDriveFileID, js.ValueOf("driveFileId"), idb.IndexOptions{
				Unique: false,
			}); idxErr != nil {
				return idxErr
			}
		}
		if !hasBases {
			if _, createErr := db.CreateObjectStore(storeBases, idb.ObjectStoreOptions{
				KeyPath: js.ValueOf("fileName"),
			}); createErr != nil {
				return createErr
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("open IndexedDB %q: %w", dbName, err)
	}

	db, err := req.Await(ctx)
	if err != nil {
		return nil, fmt.Errorf("await IndexedDB %q open: %w", dbName, err)
	}

	s.mu.Lock()
	if s.dbName == dbName && s.db == nil {
		s.db = db
	} else if s.db != nil && s.db != db {
		_ = db.Close()
		db = s.db
	}
	s.mu.Unlock()
	return db, nil
}

func (s *idbStore) marshalRecordJS(rec settings.SyncRecord) (js.Value, error) {
	raw, err := json.Marshal(rec)
	if err != nil {
		return js.Undefined(), err
	}
	return s.jsonVal.Call("parse", string(raw)), nil
}

func (s *idbStore) unmarshalRecordJS(val js.Value) (*settings.SyncRecord, error) {
	if val.IsUndefined() || val.IsNull() {
		return nil, nil
	}
	raw := s.jsonVal.Call("stringify", val).String()
	var rec settings.SyncRecord
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		return nil, fmt.Errorf("decode SyncRecord from IndexedDB: %w", err)
	}
	return &rec, nil
}

func (s *idbStore) makeBaseJS(fileName, sha256Hex, content string) js.Value {
	obj := s.objCtor.New()
	obj.Set("fileName", fileName)
	obj.Set("sha256", sha256Hex)
	obj.Set("content", content)
	return obj
}

func (s *idbStore) GetRecord(ctx context.Context, fileName string) (*settings.SyncRecord, bool, error) {
	db, err := s.openDB(ctx)
	if err != nil {
		return nil, false, err
	}
	tx, err := db.Transaction(idb.TransactionReadOnly, storeSyncRecords)
	if err != nil {
		return nil, false, err
	}
	os, err := tx.ObjectStore(storeSyncRecords)
	if err != nil {
		return nil, false, err
	}
	req, err := os.Get(js.ValueOf(fileName))
	if err != nil {
		return nil, false, err
	}
	val, err := req.Await(ctx)
	if err != nil {
		return nil, false, err
	}
	rec, err := s.unmarshalRecordJS(val)
	if err != nil || rec == nil {
		return nil, false, err
	}
	return rec, true, nil
}

func (s *idbStore) GetByDriveID(ctx context.Context, driveFileID string) (*settings.SyncRecord, bool, error) {
	if driveFileID == "" {
		return nil, false, nil
	}
	db, err := s.openDB(ctx)
	if err != nil {
		return nil, false, err
	}
	tx, err := db.Transaction(idb.TransactionReadOnly, storeSyncRecords)
	if err != nil {
		return nil, false, err
	}
	os, err := tx.ObjectStore(storeSyncRecords)
	if err != nil {
		return nil, false, err
	}
	idx, err := os.Index(indexDriveFileID)
	if err != nil {
		return nil, false, err
	}
	req, err := idx.Get(js.ValueOf(driveFileID))
	if err != nil {
		return nil, false, err
	}
	val, err := req.Await(ctx)
	if err != nil {
		return nil, false, err
	}
	rec, err := s.unmarshalRecordJS(val)
	if err != nil || rec == nil {
		return nil, false, err
	}
	return rec, true, nil
}

func (s *idbStore) ListRecords(ctx context.Context) ([]settings.SyncRecord, error) {
	db, err := s.openDB(ctx)
	if err != nil {
		return nil, err
	}
	tx, err := db.Transaction(idb.TransactionReadOnly, storeSyncRecords)
	if err != nil {
		return nil, err
	}
	os, err := tx.ObjectStore(storeSyncRecords)
	if err != nil {
		return nil, err
	}
	cursorReq, err := os.OpenCursor(idb.CursorNext)
	if err != nil {
		return nil, err
	}
	var out []settings.SyncRecord
	if err := cursorReq.Iter(ctx, func(c *idb.CursorWithValue) error {
		val, valErr := c.Value()
		if valErr != nil {
			return valErr
		}
		rec, decErr := s.unmarshalRecordJS(val)
		if decErr != nil {
			return decErr
		}
		if rec != nil {
			out = append(out, *rec)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].FileName < out[j].FileName
	})
	return out, nil
}

func (s *idbStore) PutRecordWithBase(
	ctx context.Context,
	rec settings.SyncRecord,
	updateBase bool,
	baseContent *string,
) error {
	db, err := s.openDB(ctx)
	if err != nil {
		return err
	}

	var baseVal js.Value
	hasNewBase := false
	if updateBase {
		if baseContent == nil {
			rec.HasBase = false
			rec.BaseMeta = nil
		} else {
			shaHex, md5Hex, sz := syncstore.ComputeChecksums(*baseContent)
			rec.HasBase = true
			rec.BaseMeta = &settings.BaseMeta{
				SHA256: shaHex,
				MD5:    md5Hex,
				Size:   sz,
			}
			baseVal = s.makeBaseJS(rec.FileName, shaHex, *baseContent)
			hasNewBase = true
		}
	}
	if rec.Drive != nil {
		rec.DriveFileID = rec.Drive.FileID
	} else {
		rec.DriveFileID = ""
	}
	rec.Relation = syncstore.ComputeRelation(&rec)

	recJS, err := s.marshalRecordJS(rec)
	if err != nil {
		return err
	}

	stores := []string{storeBases}
	if !updateBase {
		stores = nil
	}
	tx, err := db.Transaction(idb.TransactionReadWrite, storeSyncRecords, stores...)
	if err != nil {
		return err
	}
	recOS, err := tx.ObjectStore(storeSyncRecords)
	if err != nil {
		return err
	}
	if _, err := recOS.Put(recJS); err != nil {
		return err
	}
	if updateBase {
		baseOS, err := tx.ObjectStore(storeBases)
		if err != nil {
			return err
		}
		if hasNewBase {
			if _, err := baseOS.Put(baseVal); err != nil {
				return err
			}
		} else {
			if _, err := baseOS.Delete(js.ValueOf(rec.FileName)); err != nil {
				return err
			}
		}
	}
	return tx.Await(ctx)
}

func (s *idbStore) DeleteRecord(ctx context.Context, fileName string) error {
	db, err := s.openDB(ctx)
	if err != nil {
		return err
	}
	tx, err := db.Transaction(idb.TransactionReadWrite, storeSyncRecords, storeBases)
	if err != nil {
		return err
	}
	recOS, err := tx.ObjectStore(storeSyncRecords)
	if err != nil {
		return err
	}
	baseOS, err := tx.ObjectStore(storeBases)
	if err != nil {
		return err
	}
	key := js.ValueOf(fileName)
	if _, err := recOS.Delete(key); err != nil {
		return err
	}
	if _, err := baseOS.Delete(key); err != nil {
		return err
	}
	return tx.Await(ctx)
}

func (s *idbStore) GetBase(ctx context.Context, fileName string) (string, string, bool, error) {
	db, err := s.openDB(ctx)
	if err != nil {
		return "", "", false, err
	}
	tx, err := db.Transaction(idb.TransactionReadOnly, storeBases)
	if err != nil {
		return "", "", false, err
	}
	baseOS, err := tx.ObjectStore(storeBases)
	if err != nil {
		return "", "", false, err
	}
	req, err := baseOS.Get(js.ValueOf(fileName))
	if err != nil {
		return "", "", false, err
	}
	val, err := req.Await(ctx)
	if err != nil || val.IsUndefined() || val.IsNull() {
		return "", "", false, err
	}
	return val.Get("content").String(), val.Get("sha256").String(), true, nil
}

func (s *idbStore) MigrateKey(ctx context.Context, oldFileName, newFileName string, updated settings.SyncRecord) error {
	var oldBaseContent string
	var oldBaseSHA string
	var hasBase bool
	if updated.HasBase {
		var err error
		oldBaseContent, oldBaseSHA, hasBase, err = s.GetBase(ctx, oldFileName)
		if err != nil {
			return err
		}
	}

	db, err := s.openDB(ctx)
	if err != nil {
		return err
	}
	updated.FileName = newFileName
	if updated.Drive != nil {
		updated.DriveFileID = updated.Drive.FileID
	} else {
		updated.DriveFileID = ""
	}
	updated.HasBase = hasBase
	if hasBase && updated.BaseMeta == nil {
		_, md5Hex, sz := syncstore.ComputeChecksums(oldBaseContent)
		updated.BaseMeta = &settings.BaseMeta{SHA256: oldBaseSHA, MD5: md5Hex, Size: sz}
	}
	if !hasBase {
		updated.BaseMeta = nil
	}
	updated.Relation = syncstore.ComputeRelation(&updated)

	recJS, err := s.marshalRecordJS(updated)
	if err != nil {
		return err
	}

	tx, err := db.Transaction(idb.TransactionReadWrite, storeSyncRecords, storeBases)
	if err != nil {
		return err
	}
	recOS, err := tx.ObjectStore(storeSyncRecords)
	if err != nil {
		return err
	}
	baseOS, err := tx.ObjectStore(storeBases)
	if err != nil {
		return err
	}

	oldKey := js.ValueOf(oldFileName)
	newKey := js.ValueOf(newFileName)

	if _, err := recOS.Delete(oldKey); err != nil {
		return err
	}
	if _, err := recOS.Put(recJS); err != nil {
		return err
	}
	if _, err := baseOS.Delete(oldKey); err != nil {
		return err
	}
	if hasBase {
		if _, err := baseOS.Put(s.makeBaseJS(newFileName, oldBaseSHA, oldBaseContent)); err != nil {
			return err
		}
	} else {
		if _, err := baseOS.Delete(newKey); err != nil {
			return err
		}
	}
	return tx.Await(ctx)
}

func (s *idbStore) ClearAll(ctx context.Context) error {
	db, err := s.openDB(ctx)
	if err != nil {
		return err
	}
	tx, err := db.Transaction(idb.TransactionReadWrite, storeSyncRecords, storeBases)
	if err != nil {
		return err
	}
	recOS, err := tx.ObjectStore(storeSyncRecords)
	if err != nil {
		return err
	}
	baseOS, err := tx.ObjectStore(storeBases)
	if err != nil {
		return err
	}
	if _, err := recOS.Clear(); err != nil {
		return err
	}
	if _, err := baseOS.Clear(); err != nil {
		return err
	}
	return tx.Await(ctx)
}
