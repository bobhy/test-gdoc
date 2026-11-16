// Package syncplan provides pure-Go vault path normalization, conflict-copy naming,
// pull application, and push outcome application free of syscall/js dependencies.
package syncplan

import (
	"fmt"
	"path"
	"strings"

	"test-gdoc/go/settings"
)

// VaultFS abstracts flat-folder vault operations for pure-Go unit tests
// and JSPI Wasm runtime execution.
type VaultFS interface {
	ListFlatStats(folderPath string) ([]settings.VaultFileStat, error)
	Exists(filePath string) (bool, error)
	ReadText(filePath string) (string, bool, error)
	WriteText(filePath string, content string) error
	RenameFile(oldPath, newPath string) error
}

// PulledFile represents a flat Drive file downloaded into memory for local vault application.
type PulledFile struct {
	Name        string
	RenamedFrom string
	Content     string
}

// PullVaultResult reports local writes/renames executed during Pull.
type PullVaultResult struct {
	Pulled  int
	Renamed int
}

// PushApplyResult reports post-merge vault writes for an active-note push.
type PushApplyResult struct {
	ConflictCopyName string
}

// IsEligibleFileName returns true for flat .md or .txt filenames with no path separators.
func IsEligibleFileName(fileName string) bool {
	trimmed := strings.TrimSpace(fileName)
	if trimmed == "" || strings.ContainsAny(trimmed, `/\`) {
		return false
	}
	lower := strings.ToLower(trimmed)
	return strings.HasSuffix(lower, ".md") || strings.HasSuffix(lower, ".txt")
}

// NormalizeVaultPath mirrors Obsidian's normalizePath for POSIX vault paths.
func NormalizeVaultPath(p string) string {
	replaced := strings.ReplaceAll(p, `\`, `/`)
	cleaned := path.Clean(replaced)
	if cleaned == "." || cleaned == "/" {
		return ""
	}
	return strings.Trim(cleaned, `/`)
}

// JoinVaultPath joins a vault folder path and flat file name into a normalized path.
func JoinVaultPath(folderPath, fileName string) string {
	cleanFolder := NormalizeVaultPath(strings.TrimSpace(folderPath))
	cleanFile := NormalizeVaultPath(strings.TrimSpace(fileName))
	if cleanFolder == "" {
		return cleanFile
	}
	if cleanFile == "" {
		return cleanFolder
	}
	return cleanFolder + "/" + cleanFile
}

// SplitFileName splits "note.md" into ("note", ".md").
func SplitFileName(fileName string) (stem, ext string) {
	dot := strings.LastIndexByte(fileName, '.')
	if dot <= 0 {
		return fileName, ""
	}
	return fileName[:dot], fileName[dot:]
}

// WriteConflictCopy saves remoteContent as "<stem> (remote conflict).<ext>" (with numeric suffix
// when needed) in syncFolder and returns the created flat filename.
func WriteConflictCopy(vault VaultFS, syncFolder, fileName, remoteContent string) (string, error) {
	stem, ext := SplitFileName(fileName)
	candidate := stem + " (remote conflict)" + ext
	counter := 2
	for {
		exists, err := vault.Exists(JoinVaultPath(syncFolder, candidate))
		if err != nil {
			return "", fmt.Errorf("check conflict copy %s: %w", candidate, err)
		}
		if !exists {
			break
		}
		candidate = fmt.Sprintf("%s (remote conflict %d)%s", stem, counter, ext)
		counter++
	}
	if err := vault.WriteText(JoinVaultPath(syncFolder, candidate), remoteContent); err != nil {
		return "", fmt.Errorf("write conflict copy %s: %w", candidate, err)
	}
	return candidate, nil
}

// ApplyPulledFiles applies remote renames (when safe) and writes pulled Drive files to syncFolder.
func ApplyPulledFiles(vault VaultFS, syncFolder string, files []PulledFile) (PullVaultResult, error) {
	var res PullVaultResult
	for _, f := range files {
		if !IsEligibleFileName(f.Name) {
			continue
		}
		newPath := JoinVaultPath(syncFolder, f.Name)
		if f.RenamedFrom != "" && f.RenamedFrom != f.Name {
			oldPath := JoinVaultPath(syncFolder, f.RenamedFrom)
			if oldExists, err := vault.Exists(oldPath); err == nil && oldExists {
				if newExists, err := vault.Exists(newPath); err == nil && !newExists {
					if err := vault.RenameFile(oldPath, newPath); err != nil {
						return res, fmt.Errorf("rename %s -> %s: %w", oldPath, newPath, err)
					}
					res.Renamed++
				}
			}
		}
		if err := vault.WriteText(newPath, f.Content); err != nil {
			return res, fmt.Errorf("write pulled file %s: %w", f.Name, err)
		}
		res.Pulled++
	}
	return res, nil
}

// ApplyPushOutcome creates keepBoth conflict copies or updates the local note when Go's 3-way merge
// modified local content (e.g., clean merge or conflict markers using IndexedDB common base).
func ApplyPushOutcome(
	vault VaultFS,
	syncFolder, fileName string,
	res settings.MergeAndPushResult,
) (PushApplyResult, error) {
	if res.Outcome == "keepBoth" {
		copyName, err := WriteConflictCopy(vault, syncFolder, fileName, res.RemoteContent)
		if err != nil {
			return PushApplyResult{}, err
		}
		return PushApplyResult{ConflictCopyName: copyName}, nil
	}

	if res.LocalUpdated {
		if err := vault.WriteText(JoinVaultPath(syncFolder, fileName), res.Content); err != nil {
			return PushApplyResult{}, fmt.Errorf("write merged local file %s: %w", fileName, err)
		}
	}
	return PushApplyResult{}, nil
}
