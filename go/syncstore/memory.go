package syncstore

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"test-gdoc/go/settings"
)

type memoryBase struct {
	content string
	sha256  string
}

// MemoryStore is a thread-safe in-memory Store implementation used by native Go tests.
type MemoryStore struct {
	mu      sync.Mutex
	records map[string]settings.SyncRecord
	bases   map[string]memoryBase
}

// NewMemoryStore allocates an empty MemoryStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		records: make(map[string]settings.SyncRecord),
		bases:   make(map[string]memoryBase),
	}
}

func cloneRecord(r settings.SyncRecord) settings.SyncRecord {
	if r.Drive != nil {
		d := *r.Drive
		r.Drive = &d
	}
	if r.Vault != nil {
		v := *r.Vault
		r.Vault = &v
	}
	if r.BaseMeta != nil {
		b := *r.BaseMeta
		r.BaseMeta = &b
	}
	return r
}

func (m *MemoryStore) GetRecord(_ context.Context, fileName string) (*settings.SyncRecord, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.records[fileName]
	if !ok {
		return nil, false, nil
	}
	cp := cloneRecord(r)
	return &cp, true, nil
}

func (m *MemoryStore) GetByDriveID(_ context.Context, driveFileID string) (*settings.SyncRecord, bool, error) {
	if driveFileID == "" {
		return nil, false, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.records {
		if r.DriveFileID == driveFileID {
			cp := cloneRecord(r)
			return &cp, true, nil
		}
	}
	return nil, false, nil
}

func (m *MemoryStore) ListRecords(_ context.Context) ([]settings.SyncRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]settings.SyncRecord, 0, len(m.records))
	for _, r := range m.records {
		out = append(out, cloneRecord(r))
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].FileName < out[j].FileName
	})
	return out, nil
}

func (m *MemoryStore) PutRecordWithBase(
	_ context.Context,
	rec settings.SyncRecord,
	updateBase bool,
	baseContent *string,
) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if updateBase {
		if baseContent == nil {
			rec.HasBase = false
			rec.BaseMeta = nil
			delete(m.bases, rec.FileName)
		} else {
			shaHex, md5Hex, sz := ComputeChecksums(*baseContent)
			rec.HasBase = true
			rec.BaseMeta = &settings.BaseMeta{
				SHA256: shaHex,
				MD5:    md5Hex,
				Size:   sz,
			}
			m.bases[rec.FileName] = memoryBase{content: *baseContent, sha256: shaHex}
		}
	}
	if rec.Drive != nil {
		rec.DriveFileID = rec.Drive.FileID
	} else {
		rec.DriveFileID = ""
	}
	rec.Relation = ComputeRelation(&rec)
	m.records[rec.FileName] = cloneRecord(rec)
	return nil
}

func (m *MemoryStore) DeleteRecord(_ context.Context, fileName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.records, fileName)
	delete(m.bases, fileName)
	return nil
}

func (m *MemoryStore) GetBase(_ context.Context, fileName string) (string, string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.bases[fileName]
	if !ok {
		return "", "", false, nil
	}
	return b.content, b.sha256, true, nil
}

func (m *MemoryStore) MigrateKey(_ context.Context, oldFileName, newFileName string, updated settings.SyncRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.records[oldFileName]; !ok {
		return fmt.Errorf("cannot migrate missing record %q", oldFileName)
	}
	if updated.Drive != nil {
		updated.DriveFileID = updated.Drive.FileID
	} else {
		updated.DriveFileID = ""
	}
	if b, hasBase := m.bases[oldFileName]; hasBase {
		delete(m.bases, oldFileName)
		m.bases[newFileName] = b
		updated.HasBase = true
		if updated.BaseMeta == nil {
			_, md5Hex, sz := ComputeChecksums(b.content)
			updated.BaseMeta = &settings.BaseMeta{SHA256: b.sha256, MD5: md5Hex, Size: sz}
		}
	} else {
		delete(m.bases, newFileName)
		updated.HasBase = false
		updated.BaseMeta = nil
	}
	delete(m.records, oldFileName)
	updated.FileName = newFileName
	updated.Relation = ComputeRelation(&updated)
	m.records[newFileName] = cloneRecord(updated)
	return nil
}

func (m *MemoryStore) ClearAll(_ context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records = make(map[string]settings.SyncRecord)
	m.bases = make(map[string]memoryBase)
	return nil
}
