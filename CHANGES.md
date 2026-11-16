# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.2.0]

### Added
- **IndexedDB sync state store**: Per-vault IndexedDB database (`test-gdoc-<slug>`) via `github.com/hack-pad/go-indexeddb/idb` (`sync_records` store with `byDriveFileId` index and `bases` store) tracking Drive metadata, Vault metadata, common base snapshots, and computed sync relations.
- **Sync State Inspector UI**: Added `SyncStateView` and `BaseEditorModal` to inspect per-file sync state, view/edit/clear base snapshots, and run an IndexedDB microbenchmark.
- **Delete + create rename detection**: Vault `create`, `delete`, and `modify` hints paired with SHA-256 correlation and Drive `RenameFile` (`PATCH` metadata), without relying on `vault.on('rename')`.
- **Platform JSPI startup check**: Detects `Platform.isIosApp` or missing `WebAssembly.Suspending`/`WebAssembly.promising` during `onload()` and surfaces a persistent `Notice`.
- **Benchmark harness**: Added `benchmarks/run-bench.mjs` (`fake-indexeddb/auto` + isolated Node subprocesses) comparing bundle sizes and sync/merge latencies against cached `main` artifacts.

### Changed
- **Hybrid JSPI & single-root host interop**: Migrated Vault I/O and direct merge paths to `//go:wasmimport obsidianHost` + `//go:wasmexport` linear-memory transfers, moved HTTP transport directly onto `obsidian.requestUrl` with `ArrayBuffer`, and serialized all Go Wasm entries through a FIFO mutex.
- **IndexedDB `GetAll()` scans**: Updated `idbStore.ListRecords` to use `ObjectStore.GetAll()` with a single `JSON.stringify` call instead of cursor iteration (using PR fork of `go-indexeddb`).

### Removed
- **Vault base-snapshot folder**: Removed `BaseSnapshotFolderPath` (`test-gdoc-base`) in favor of storing common base snapshots in IndexedDB.

## [0.1.0]

### Added
- First draft of gdrive api and conflict resolution playground.

## [0.0.2]

### Added
- `CHANGES.md` following the Keep a Changelog format.

### Changed
- Updated toolchain and `devDependencies` for Node 26 LTS, bumped `minAppVersion` to `1.13.0` for declarative settings, and updated `LICENSE` copyright with `obsidian-sample-plugin` attribution.
