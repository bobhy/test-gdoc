package syncplan

import (
	"testing"

	"test-gdoc/go/settings"
)

type memVault struct {
	files map[string]string
}

func newMemVault(initial map[string]string) *memVault {
	m := make(map[string]string, len(initial))
	for k, v := range initial {
		m[NormalizeVaultPath(k)] = v
	}
	return &memVault{files: m}
}

func (m *memVault) ListFlatStats(folderPath string) ([]settings.VaultFileStat, error) {
	prefix := NormalizeVaultPath(folderPath)
	var out []settings.VaultFileStat
	for p, body := range m.files {
		name := ""
		if prefix == "" {
			if !stringsContainsSlash(p) {
				name = p
			}
		} else if len(p) > len(prefix)+1 && p[:len(prefix)] == prefix && p[len(prefix)] == '/' {
			rest := p[len(prefix)+1:]
			if !stringsContainsSlash(rest) {
				name = rest
			}
		}
		if name != "" && IsEligibleFileName(name) {
			out = append(out, settings.VaultFileStat{
				Name:    name,
				Path:    p,
				Size:    int64(len(body)),
				MTimeMs: 100,
			})
		}
	}
	return out, nil
}

func stringsContainsSlash(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			return true
		}
	}
	return false
}

func (m *memVault) Exists(filePath string) (bool, error) {
	_, ok := m.files[NormalizeVaultPath(filePath)]
	return ok, nil
}

func (m *memVault) ReadText(filePath string) (string, bool, error) {
	v, ok := m.files[NormalizeVaultPath(filePath)]
	return v, ok, nil
}

func (m *memVault) WriteText(filePath string, content string) error {
	m.files[NormalizeVaultPath(filePath)] = content
	return nil
}

func (m *memVault) RenameFile(oldPath, newPath string) error {
	o := NormalizeVaultPath(oldPath)
	n := NormalizeVaultPath(newPath)
	v, ok := m.files[o]
	if ok {
		delete(m.files, o)
		m.files[n] = v
	}
	return nil
}

func TestPathHelpers(t *testing.T) {
	if !IsEligibleFileName("note.md") || !IsEligibleFileName("a.TXT") {
		t.Fatal("expected .md and .txt files to be eligible")
	}
	if IsEligibleFileName("sub/note.md") || IsEligibleFileName("img.png") || IsEligibleFileName(" ") {
		t.Fatal("unexpected eligibility for invalid filenames")
	}
	if got := JoinVaultPath("/test-gdoc-sync/", "note.md"); got != "test-gdoc-sync/note.md" {
		t.Fatalf("JoinVaultPath = %q", got)
	}
	if got := JoinVaultPath("", "note.md"); got != "note.md" {
		t.Fatalf("JoinVaultPath root = %q", got)
	}
	stem, ext := SplitFileName("my.note.md")
	if stem != "my.note" || ext != ".md" {
		t.Fatalf("SplitFileName = (%q, %q)", stem, ext)
	}
}

func TestApplyPulledFilesHandlesDriveRename(t *testing.T) {
	v := newMemVault(map[string]string{
		"test-gdoc-sync/old.md": "old content",
	})

	res, err := ApplyPulledFiles(v, "test-gdoc-sync", []PulledFile{
		{Name: "new.md", RenamedFrom: "old.md", Content: "updated remote"},
		{Name: "brandnew.md", Content: "hello"},
	})
	if err != nil {
		t.Fatalf("ApplyPulledFiles: %v", err)
	}
	if res.Pulled != 2 || res.Renamed != 1 {
		t.Fatalf("res = %+v, want pulled=2 renamed=1", res)
	}
	if _, exists := v.files["test-gdoc-sync/old.md"]; exists {
		t.Fatal("old.md should have been renamed to new.md")
	}
	if v.files["test-gdoc-sync/new.md"] != "updated remote" {
		t.Fatalf("new.md = %q", v.files["test-gdoc-sync/new.md"])
	}
}

func TestApplyPushOutcomeKeepBothAndClean(t *testing.T) {
	v := newMemVault(map[string]string{
		"test-gdoc-sync/note.md":                 "local edit",
		"test-gdoc-sync/note (remote conflict).md": "older conflict",
	})

	applied, err := ApplyPushOutcome(
		v,
		"test-gdoc-sync",
		"note.md",
		settings.MergeAndPushResult{
			Outcome:       "keepBoth",
			RemoteExisted: true,
			RemoteContent: "remote edit",
		},
	)
	if err != nil {
		t.Fatalf("ApplyPushOutcome keepBoth: %v", err)
	}
	if applied.ConflictCopyName != "note (remote conflict 2).md" {
		t.Fatalf("conflict copy = %q", applied.ConflictCopyName)
	}
	if v.files["test-gdoc-sync/note (remote conflict 2).md"] != "remote edit" {
		t.Fatalf("missing conflict copy content")
	}

	_, err = ApplyPushOutcome(
		v,
		"test-gdoc-sync",
		"note.md",
		settings.MergeAndPushResult{
			Outcome:      "clean",
			LocalUpdated: true,
			Content:      "merged text",
		},
	)
	if err != nil {
		t.Fatalf("ApplyPushOutcome clean: %v", err)
	}
	if v.files["test-gdoc-sync/note.md"] != "merged text" {
		t.Fatalf("merged content not applied: %q", v.files["test-gdoc-sync/note.md"])
	}
}
