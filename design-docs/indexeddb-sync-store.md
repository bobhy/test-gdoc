# IndexedDB Synchronization State Store Design

## Overview

Replaces the Vault-visible `test-gdoc-base` snapshot folder with a per-vault browser `IndexedDB` state store driven directly from Go WebAssembly via `github.com/hack-pad/go-indexeddb/idb`, backed by a pure-Go functional core (`go/syncstore`) tested natively under `go test ./go/...`.

## Storage Schema (`test-gdoc-<vaultSlug>`, DB v1)

Two object stores live inside the same per-vault IndexedDB database so updates execute atomically inside single `readwrite` transactions (`tx.Await(ctx)`):

1. **`sync_records`** (keyPath: `fileName`, secondary index `byDriveFileId` on `driveFileId`, non-unique or unique optional)
   - `fileName` (`string`): canonical flat filename (`.md` / `.txt`) in `localSyncFolderPath` / `driveFolderPath`.
   - `driveFileId` (`string`): top-level indexed copy of `drive.fileId` (`""` when unknown on Drive) so Drive renames resolve via index lookup.
   - `drive` (`*DriveFileState`): optional `{ fileId, name, mimeType, modifiedTime, size, sha256Checksum, md5Checksum }`.
   - `vault` (`*VaultFileState`): optional `{ path, size, mtimeMs, sha256, deletedAt }`.
   - `baseMeta` (`*BaseMeta`): optional `{ sha256, size }` (`null` when `hasBase === false`).
   - `hasBase` (`bool`): explicit presence flag.
   - `relation` (`SyncRelation`): computed state (`inSync`, `localOnly`, `remoteOnly`, `localModified`, `remoteModified`, `bothModified`, `unseededBoth`, `localRenamed`, `remoteRenamed`).
   - `renamedFrom` (`string`, optional): prior filename when a rename correlation matched.
   - `updatedAt` (`string`): RFC3339 timestamp.
2. **`bases`** (keyPath: `fileName`)
   - `{ fileName, sha256, content }`.
   - **Null-base optimization:** No entry exists in `bases` when `hasBase === false`, and listing/scanning `sync_records` (`GetAll`) never reads or deserializes base text blobs.
   - **Referential integrity:** Every `PutRecordWithBase`, `MigrateKey`, or `DeleteRecord` updates or removes both `sync_records` and `bases` within the same `readwrite` transaction.

## Change Detection, Checksums & Renames

- **Google Drive:** [go/gdrive/client.go](../go/gdrive/client.go) requests `sha256Checksum,md5Checksum` on `files.list` and `files.get`.
- **Obsidian Vault:** JSPI `obsidianHost.vaultListFlat` returns lightweight `{ name, mtimeMs, size }` metadata (`TFile.stat`). Go compares `(mtimeMs, size)` against `SyncRecord.vault` and only invokes JSPI `vaultRead` to compute `crypto/sha256` when `(mtimeMs, size)` changed or no Vault record exists.
- **No `vault.on('rename')` hook:** Live `vault.on('create')`, `vault.on('delete')`, and `vault.on('modify')` notifications record lightweight delete/create hints. When an existing local record disappears (or flags deleted) and a new local file matches its `vault.sha256` cleanly, `go/syncstore` atomically migrates `(sync_record, base)` to `newFileName` while keeping the Drive `fileId` association. Drive renames resolve directly by `drive.fileId`.

## Hybrid JSPI Safety (`REG_SP`)

Because `hack-pad/go-indexeddb` uses `syscall/js` callbacks, all IDB operations execute during the `syscall/js` `invoke` phase behind `GoWasmBridge`'s FIFO `enqueue` mutex, strictly separated from `WebAssembly.promising` JSPI `Vault` calls.

## Node Testing & Benchmarking

`fake-indexeddb` (`devDependency`) provides W3C `globalThis.indexedDB` in Node 26 so `benchmarks/run-bench.mjs` exercises the compiled `hack-pad/go-indexeddb` Wasm code alongside `baseline-main`.
