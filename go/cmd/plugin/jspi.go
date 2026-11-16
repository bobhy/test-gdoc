//go:build js && wasm

package main

import (
	"encoding/json"
	"errors"
	"structs"
	"sync"
	"unsafe"

	"test-gdoc/go/merge"
	"test-gdoc/go/settings"
)

// SliceHeader points to a pinned Go []byte buffer inside Wasm linear memory.
type SliceHeader struct {
	_   structs.HostLayout
	Ptr uint32
	Len uint32
}

// MergeDirectHeader holds the synchronous linear-memory output of reconcileTextDirect.
type MergeDirectHeader struct {
	_       structs.HostLayout
	Outcome int32 // 0=clean, 1=folded, 2=keepBoth
	BufID   uint32
	Ptr     uint32
	Len     uint32
}

var (
	pinMu       sync.Mutex
	pinnedBuf   = map[uint32][]byte{}
	nextBufID   uint32
	scratchHdr  SliceHeader
	mergeHdr    MergeDirectHeader
	lastJspiErr string
)

//go:wasmimport obsidianHost vaultListStats
func hostVaultListStats(folderPath string, out *SliceHeader) int32

//go:wasmimport obsidianHost vaultExists
func hostVaultExists(filePath string) int32

//go:wasmimport obsidianHost vaultRead
func hostVaultRead(filePath string, out *SliceHeader) int32

//go:wasmimport obsidianHost vaultWrite
func hostVaultWrite(filePath string, content string) int32

//go:wasmimport obsidianHost vaultRename
func hostVaultRename(oldPath string, newPath string) int32

//go:wasmexport getScratchHeader
func getScratchHeader() uint32 {
	return uint32(uintptr(unsafe.Pointer(&scratchHdr)))
}

//go:wasmexport getMergeDirectHeader
func getMergeDirectHeader() uint32 {
	return uint32(uintptr(unsafe.Pointer(&mergeHdr)))
}

//go:wasmexport allocBuffer
func allocBuffer(size uint32, out *SliceHeader) uint32 {
	return pinSlice(make([]byte, size), out)
}

//go:wasmexport releaseBuffer
func releaseBuffer(id uint32) {
	takePinnedBuffer(id)
}

//go:wasmexport getLastJspiError
func getLastJspiError(out *SliceHeader) uint32 {
	pinMu.Lock()
	msg := lastJspiErr
	pinMu.Unlock()
	if msg == "" {
		out.Ptr = 0
		out.Len = 0
		return 0
	}
	return pinSlice([]byte(msg), out)
}

func pinSlice(b []byte, out *SliceHeader) uint32 {
	if len(b) == 0 {
		out.Ptr = 0
		out.Len = 0
		return 0
	}
	pinMu.Lock()
	nextBufID++
	id := nextBufID
	pinnedBuf[id] = b
	pinMu.Unlock()

	out.Ptr = uint32(uintptr(unsafe.Pointer(&b[0])))
	out.Len = uint32(len(b))
	return id
}

func takePinnedBuffer(id uint32) []byte {
	if id == 0 {
		return nil
	}
	pinMu.Lock()
	b := pinnedBuf[id]
	delete(pinnedBuf, id)
	pinMu.Unlock()
	return b
}

func setLastJspiError(err error) {
	pinMu.Lock()
	if err != nil {
		lastJspiErr = err.Error()
	} else {
		lastJspiErr = ""
	}
	pinMu.Unlock()
}

type jspiVaultFS struct{}

func (jspiVaultFS) ListFlatStats(folderPath string) ([]settings.VaultFileStat, error) {
	bufID := hostVaultListStats(folderPath, &scratchHdr)
	if bufID < 0 {
		return nil, errors.New("obsidianHost.vaultListStats failed")
	}
	raw := takePinnedBuffer(uint32(bufID))
	if len(raw) == 0 {
		return nil, nil
	}
	var out []settings.VaultFileStat
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (jspiVaultFS) Exists(filePath string) (bool, error) {
	rc := hostVaultExists(filePath)
	if rc < 0 {
		return false, errors.New("obsidianHost.vaultExists failed")
	}
	return rc == 1, nil
}

func (jspiVaultFS) ReadText(filePath string) (string, bool, error) {
	bufID := hostVaultRead(filePath, &scratchHdr)
	if bufID == -2 {
		return "", false, nil
	}
	if bufID < 0 {
		return "", false, errors.New("obsidianHost.vaultRead failed")
	}
	return string(takePinnedBuffer(uint32(bufID))), true, nil
}

func (jspiVaultFS) WriteText(filePath string, content string) error {
	if hostVaultWrite(filePath, content) != 0 {
		return errors.New("obsidianHost.vaultWrite failed")
	}
	return nil
}

func (jspiVaultFS) RenameFile(oldPath, newPath string) error {
	if hostVaultRename(oldPath, newPath) != 0 {
		return errors.New("obsidianHost.vaultRename failed")
	}
	return nil
}

//go:wasmexport applyPullBatch
func applyPullBatch(batchID uint32, syncFolderBufID uint32, out *SliceHeader) uint32 {
	syncFolder := string(takePinnedBuffer(syncFolderBufID))
	if activeService == nil {
		setLastJspiError(errors.New("bridge service not initialized"))
		return 0
	}
	sum, err := activeService.ApplyPullBatch(jspiVaultFS{}, int(batchID), syncFolder)
	if err != nil {
		setLastJspiError(err)
		return 0
	}
	raw, err := json.Marshal(sum)
	if err != nil {
		setLastJspiError(err)
		return 0
	}
	setLastJspiError(nil)
	return pinSlice(raw, out)
}

//go:wasmexport preparePushInputs
func preparePushInputs(syncFolderBufID, fileNameBufID uint32) uint32 {
	syncFolder := string(takePinnedBuffer(syncFolderBufID))
	fileName := string(takePinnedBuffer(fileNameBufID))
	if activeService == nil {
		setLastJspiError(errors.New("bridge service not initialized"))
		return 0
	}
	batchID, err := activeService.PreparePushInputs(jspiVaultFS{}, syncFolder, fileName)
	if err != nil {
		setLastJspiError(err)
		return 0
	}
	setLastJspiError(nil)
	return uint32(batchID)
}

//go:wasmexport applyPreparedPush
func applyPreparedPush(batchID uint32, out *SliceHeader) uint32 {
	if activeService == nil {
		setLastJspiError(errors.New("bridge service not initialized"))
		return 0
	}
	sum, err := activeService.ApplyPreparedPush(jspiVaultFS{}, int(batchID))
	if err != nil {
		setLastJspiError(err)
		return 0
	}
	raw, err := json.Marshal(sum)
	if err != nil {
		setLastJspiError(err)
		return 0
	}
	setLastJspiError(nil)
	return pinSlice(raw, out)
}

//go:wasmexport collectVaultStats
func collectVaultStats(syncFolderBufID uint32) uint32 {
	syncFolder := string(takePinnedBuffer(syncFolderBufID))
	if activeService == nil {
		setLastJspiError(errors.New("bridge service not initialized"))
		return 0
	}
	scanID, err := activeService.CollectVaultStats(jspiVaultFS{}, syncFolder)
	if err != nil {
		setLastJspiError(err)
		return 0
	}
	setLastJspiError(nil)
	return uint32(scanID)
}

//go:wasmexport readVaultFilesForScan
func readVaultFilesForScan(scanID uint32) int32 {
	if activeService == nil {
		setLastJspiError(errors.New("bridge service not initialized"))
		return -1
	}
	if err := activeService.ReadVaultFilesForScan(jspiVaultFS{}, int(scanID)); err != nil {
		setLastJspiError(err)
		return -1
	}
	setLastJspiError(nil)
	return 0
}

//go:wasmexport reconcileTextDirect
func reconcileTextDirect(baseBufID, localBufID, remoteBufID uint32, out *MergeDirectHeader) int32 {
	baseText := string(takePinnedBuffer(baseBufID))
	localText := string(takePinnedBuffer(localBufID))
	remoteText := string(takePinnedBuffer(remoteBufID))

	res := merge.ReconcileText(baseText, localText, remoteText)
	switch res.Kind {
	case "clean":
		out.Outcome = 0
	case "folded":
		out.Outcome = 1
	default:
		out.Outcome = 2
	}

	var sliceHdr SliceHeader
	out.BufID = pinSlice([]byte(res.Content), &sliceHdr)
	out.Ptr = sliceHdr.Ptr
	out.Len = sliceHdr.Len
	return out.Outcome
}
