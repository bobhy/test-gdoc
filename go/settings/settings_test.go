package settings

import (
	"encoding/json"
	"testing"
)

func TestDefaultSettingsJSONRoundTrip(t *testing.T) {
	want := DefaultSettings()
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	var got PluginSettings
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}
	if got != want {
		t.Fatalf("round-trip mismatch: got %+v, want %+v", got, want)
	}
}

func TestNormalizeSettings(t *testing.T) {
	tests := []struct {
		name string
		in   PluginSettings
		want PluginSettings
	}{
		{
			name: "defaults unchanged",
			in:   DefaultSettings(),
			want: DefaultSettings(),
		},
		{
			name: "empty strings and non-positive maxPendingRequests fall back to defaults",
			in: PluginSettings{
				DriveFolderPath:        "   ",
				LocalSyncFolderPath:    "",
				BaseSnapshotFolderPath: "\t",
				MaxPendingRequests:     0,
			},
			want: DefaultSettings(),
		},
		{
			name: "clamps high maxPendingRequests",
			in: PluginSettings{
				DriveFolderPath:        "/custom",
				LocalSyncFolderPath:    "local",
				BaseSnapshotFolderPath: "base",
				MaxPendingRequests:     99,
			},
			want: PluginSettings{
				DriveFolderPath:        "/custom",
				LocalSyncFolderPath:    "local",
				BaseSnapshotFolderPath: "base",
				MaxPendingRequests:     10,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := NormalizeSettings(tc.in)
			if got != tc.want {
				t.Errorf("NormalizeSettings() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestDTOJSONRoundTrip(t *testing.T) {
	want := ListFilesResult{
		FolderID: "folder-123",
		Files: []FileMeta{
			{
				ID:           "file-1",
				Name:         "note.md",
				MimeType:     "text/markdown",
				ModifiedTime: "2026-11-12T00:00:00Z",
				Size:         "42",
			},
		},
		RotatedRefreshToken: "new-rt",
	}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}
	var got ListFilesResult
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}
	if got.FolderID != want.FolderID || len(got.Files) != 1 || got.Files[0] != want.Files[0] || got.RotatedRefreshToken != want.RotatedRefreshToken {
		t.Fatalf("round-trip mismatch: got %+v, want %+v", got, want)
	}
}
