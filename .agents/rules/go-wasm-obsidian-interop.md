# Go/Wasm ↔ Obsidian Interop Architecture Rules (Draft)

> **Status**: Draft architectural guidelines distilled from Go 1.27 `syscall/js` internals, Obsidian plugin ecosystem practices, and V8 JSPI benchmarks on `test-gdoc`. Refine as patterns are proven in production.

## Why Boundary Crossing Is Expensive in `GOOS=js GOARCH=wasm`

In Go's `js/wasm` runtime (`syscall/js` + `wasm_exec.js`), Wasm linear memory and the V8 JavaScript heap are completely separate:

1. **Every non-primitive `js.Value` allocates on both heaps**: `wasm_exec.js` (`storeValue`) inserts the JS object into `this._values` / `this._ids` / `this._goRefCounts`, and Go (`makeValue`) allocates a `*ref` with `runtime.SetFinalizer` to later invoke `syscall/js.finalizeRef`.
2. **Reading a single `string` property (`v.Get("prop").String()`) takes ~5 Wasm↔JS crossings**: `valueGet` (`TextDecoder` for key + `Reflect.get` + ref box) → `valuePrepareString` (`TextEncoder` + temporary `Uint8Array` box) → `valueLoadString` → `finalizeRef` (temp array) → deferred GC `finalizeRef` (property value).
3. **Every JS→Go callback (`js.FuncOf`) costs 7+ Wasm→JS crossings**: `wasm_exec.js` `_makeFuncWrapper` sets `go._pendingEvent` and calls `resume()`; Go's `handleEvent` calls back into JS via `Get("_pendingEvent")`, `Set("_pendingEvent", Null())`, `Get("id")`, `Get("this")`, `Get("args")`, `Length()`, `Index(i)`, and `Set("result", ...)`.
4. **Main-thread async deadlock risk**: Calling a Promise-returning JS API cannot block the Wasm stack directly under standard `syscall/js`; it must attach `resolve`/`reject` `js.FuncOf` handlers, park the goroutine on a Go channel, and yield back to the JS event loop.

---

## Four Core Interop Rules (Non-Worker Architecture)

### Rule 1: Keep Data Schemas Go-First — Never Unpack TS Objects Field-by-Field

- **Do**: Define shared data-transfer structs in Go (`go/settings/settings.go`) and generate TypeScript interfaces from them (`go/cmd/gents` via `npm run build:wasm`). Cross the boundary in one bulk JSON or binary payload.
- **Do not**: Mirror Obsidian's TypeScript data types (`TFile`, `CachedMetadata`, `FrontMatterCache`) as Go structs populated field-by-field via `val.Get("...")` in loops.
- **Exception (Opaque Handles)**: Only wrap Obsidian TS types in Go when they act as **opaque service or resource handles** (`type Vault struct { val js.Value }`, `type TFile struct { val js.Value }`) whose fields Go does **not** unpack, passing the handle directly back to JS method calls (`vault.val.Call("read", file.val)`).

### Rule 2: Eliminate Per-Method JSON "Fat Thunks" via Single-Root Injection + Generic `Await`

- **Do not** write a bespoke TypeScript host adapter function and JSON schema for every Obsidian API call when Go needs direct imperative access to `App`, `Vault`, `Workspace`, `SecretStorage`, or `Notice`.
- **Do**: Pass `this.app` (and a reference to the `'obsidian'` module if Go needs `Notice`) to Go **once** during bridge initialization, store it once in Go (avoiding repeated `js.Global().Get(...)` ref-table churn), and use a single generic `Await(ctx, promise)` helper in Go.
- **Browser `IndexedDB`**: `window.indexedDB` is already on `js.Global()`. Prefer community packages such as `hack-pad/go-indexeddb` or `hack-pad/hackpadfs/indexeddb` (`io/fs` backed by IndexedDB) over custom TypeScript IDB thunks.
- **UI Subclassing Constraint**: `Modal`, `ItemView`, and `PluginSettingTab` require ES6 class inheritance (`class MyModal extends Modal`); keep those shells in TypeScript and delegate lifecycle/actions to Go.

### Rule 3: Use Bulk `Uint8Array` / Linear Memory Transfers on Hot Paths

- On large payloads (note contents, file attachments, binary blobs, high-frequency local queries), avoid both field-by-field `syscall/js` getters and reflection-heavy `encoding/json` string marshaling.
- **Standard `syscall/js`**: Pass `Uint8Array` views via `js.CopyBytesToGo` / `js.CopyBytesToJS` (as in `go/cmd/plugin/roundtripper.go`). For structured binary payloads, prefer code-generated reflection-free formats (`msgp`, `vtprotobuf`, or FlatBuffers) over `encoding/json`.
- **Direct Wasm Exports (`//go:wasmexport` in Go 1.24+)**: Pure-compute synchronous functions can use `//go:wasmexport` with `structs.HostLayout` structs and linear-memory pointers, bypassing `wasm_exec.js`'s `_pendingEvent` dispatch entirely.
- **JSPI (Desktop + Android)**: When targeting Obsidian Desktop (installer `>= 1.10.0`) and Android (Chromium 137+) without iOS support, use `//go:wasmimport` + `//go:wasmexport` with `WebAssembly.Suspending` / `WebAssembly.promising` (see skill `go-wasm-jspi`) for ~20x+ faster async boundary crossings.

### Rule 4: Maintain a Pure-Go Functional Core and a TypeScript Imperative Shell

- Keep business logic (sync/merge engines, retries, auth state, parsers, validators) in pure Go packages **free of `syscall/js` imports** (e.g., `go/gdrive`, `go/merge`, `go/settings`), abstracting host I/O behind narrow Go interfaces (like `gdrive.DoFunc`).
- Keep Obsidian plugin lifecycle (`src/main.ts`), `Modal`/`ItemView` UI, `registerEvent` hooks, and batched Vault operations in a thin TypeScript imperative shell (`src/vault/sync-adapter.ts`).
- **Rule of thumb**: Aim for ≥90% of domain logic to run under native `go test ./go/...` without an Electron/browser harness.

---

## Choosing the Right Interop Mechanism

| Workload | Recommended Mechanism | Why |
| :--- | :--- | :--- |
| Low-frequency structured RPC / settings / commands | **Go-first structs + `gents` + JSON RPC** | Minimal boilerplate, type-safe across Go & TS, fast enough for human-triggered actions. |
| Imperative Obsidian API calls from Go (`Vault`, `SecretStorage`, `Notice`) | **Single-root `this.app` + generic `Await(ctx, promise)`** | Zero per-API TypeScript glue; avoids maintaining redundant host wrappers. |
| Browser persistent cache / local store | **`hack-pad/go-indexeddb` or `hackpadfs/indexeddb`** | Direct `js.Global().Get("indexedDB")` access with standard Go `io/fs` ergonomics. |
| Large binary/text transfers or high-frequency synchronous compute | **`Uint8Array` (`CopyBytesToGo`/`JS`) or `//go:wasmexport`** | Eliminates UTF-8 `TextDecoder`/`TextEncoder` churn and `encoding/json` reflection. |
| High-frequency async local loops (Desktop `>= 1.10.0` + Android; no iOS) | **JSPI (`//go:wasmimport` + `WebAssembly.Suspending`) + TS FIFO mutex** | Measured ~23x faster than `Promise.then` + `syscall/js` channels; synchronous Go call style. |
