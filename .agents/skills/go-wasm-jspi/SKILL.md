---
name: go-wasm-jspi
description: >-
  Draft guide and implementation recipe for using WebAssembly JavaScript Promise
  Integration (JSPI: WebAssembly.Suspending and WebAssembly.promising) with Go
  (GOOS=js GOARCH=wasm) in Obsidian plugins today. Includes platform restrictions,
  earliest required Obsidian/Chromium versions, linear-memory buffer passing, and
  mandatory single-flight mutex guardrails against Go REG_SP stack corruption.
---

# Go/Wasm with WebAssembly JSPI in Obsidian (Draft)

Use this skill when building or optimizing high-frequency async Go↔Obsidian calls using **WebAssembly JavaScript Promise Integration (JSPI)** (`WebAssembly.Suspending` and `WebAssembly.promising`) on Go 1.24+ (`GOOS=js GOARCH=wasm`).

JSPI eliminates `js.FuncOf` callback allocations, `wasm_exec.js` `_pendingEvent`/`_resume()` round-trips, and `_values` reference-table boxing—delivering ~23x faster async boundary crossings (~2 µs vs. ~48 µs per round-trip in Go 1.27 / V8 benchmarks).

---

## 1. Platform & Version Restrictions (Critical)

### Supported vs. Unsupported Platforms
| Platform | Engine | JSPI Status |
| :--- | :--- | :--- |
| **Windows / macOS / Linux** | Electron / Chromium V8 | **Supported** (Chromium 137+) |
| **Android** | Android System WebView (Chromium V8) | **Supported** (WebView 137+; auto-updates via Google Play on Android 10+) |
| **iOS / iPadOS** | WebKit / JavaScriptCore | **NOT supported** (do not enable JSPI if iOS compatibility is required) |

### Earliest Required Versions
- **Chromium / V8**: **Chromium 137+** (where `WebAssembly.Suspending` and `WebAssembly.promising` shipped enabled by default without experimental flags).
- **Obsidian Desktop Installer**: **v1.10.0+** minimum (bundles Electron ~37 / Chromium 138; v1.13–1.15 bundles Electron 43 / Chromium 150).
- **Go Toolchain**: **Go 1.24+** required for `//go:wasmexport` (this repo uses Go 1.27).

### The `minAppVersion` vs. Installer Trap
Obsidian's in-app auto-updater updates the Obsidian JS bundle, **not** the underlying Electron/Chromium shell. A user can have Obsidian app `1.14.x` running on an older Electron/Chromium installer where `WebAssembly.Suspending` is `undefined`.

**Mandatory startup check**: Always set `"minAppVersion"` in `manifest.json` to at least `"1.10.0"` *and* perform a runtime capability check in TypeScript before instantiating JSPI imports:

```typescript
import { Notice } from 'obsidian';

export function assertJspiSupported(): void {
	const hasSuspending = typeof (WebAssembly as unknown as { Suspending?: unknown }).Suspending === 'function';
	const hasPromising = typeof (WebAssembly as unknown as { promising?: unknown }).promising === 'function';
	if (!hasSuspending || !hasPromising) {
		const msg =
			'This plugin requires WebAssembly JSPI (Obsidian desktop installer v1.10.0+ or Android WebView 137+). Please download and run the latest Obsidian installer.';
		new Notice(msg, 10000);
		throw new Error(msg);
	}
}
```

---

## 2. Go Implementation Recipe (`//go:wasmimport` + `//go:wasmexport`)

Both `//go:wasmimport` and `//go:wasmexport` work under `GOOS=js GOARCH=wasm`, but only accept primitive numeric types (`bool`, `int32`/`uint32`, `int64`/`uint64`, `float32`/`float64`), `string` (parameter only, lowered to `(ptr, len)`), `unsafe.Pointer`, or pointers to `structs.HostLayout` structs.

### 2A. Shared Memory Header & Buffer Allocator
Use a `structs.HostLayout` header plus an exported allocator so JS `Suspending` functions can allocate exact-sized Go `[]byte` buffers inside linear memory without `syscall/js` copying:

```go
//go:build js && wasm

package main

import (
	"structs"
	"sync"
	"unsafe"
)

// SliceHeader is written into Go linear memory by the JS host import.
type SliceHeader struct {
	_   structs.HostLayout
	Ptr uint32
	Len uint32
}

var (
	pinMu         sync.Mutex
	pinnedBuf     = map[uint32][]byte{}
	nextBufID     uint32
	scratchHeader SliceHeader
)

//go:wasmexport getScratchHeader
func getScratchHeader() uint32 {
	return uint32(uintptr(unsafe.Pointer(&scratchHeader)))
}

//go:wasmexport allocBuffer
func allocBuffer(size uint32, out *SliceHeader) uint32 {
	if size == 0 {
		out.Ptr = 0
		out.Len = 0
		return 0
	}
	b := make([]byte, size)
	pinMu.Lock()
	nextBufID++
	id := nextBufID
	pinnedBuf[id] = b
	pinMu.Unlock()

	out.Ptr = uint32(uintptr(unsafe.Pointer(&b[0])))
	out.Len = size
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
```

> **Why pin the buffer?** Holding `b` in `pinnedBuf` prevents Go's GC from collecting the slice between `allocBuffer()` returning to JS and JS returning control back to Go. Always call `takePinnedBuffer(id)` immediately upon resuming.

### 2B. Calling Async Host APIs Synchronously from Go

```go
//go:wasmimport obsidianHost vaultRead
func hostVaultRead(path string, out *SliceHeader) int32 // returns bufID (>= 0) or -1 on error

//go:wasmexport processVaultNote
func processVaultNote(pathBufID uint32) int32 {
	path := string(takePinnedBuffer(pathBufID))
	var hdr SliceHeader
	bufID := hostVaultRead(path, &hdr) // Suspends transparently via V8 JSPI!
	if bufID < 0 {
		return -1
	}
	content := takePinnedBuffer(uint32(bufID))
	// Process `content` synchronously in pure Go...
	return int32(len(content))
}
```

> Keep `func main()` alive with `select {}` (or a `done` channel closed by `shutdown`) and initialize via `go.run(instance)` before invoking `WebAssembly.promising(...)` exports, because Go's `wasmexport` wrappers abort if called before runtime init (`SP == 0`).

---

## 3. TypeScript Host Wiring & Mandatory Single-Flight Mutex

### 3A. Current Non-Reentrancy Restriction (Go 1.26–1.27)
Even though V8's JSPI supports multiple suspended fibers, Go's `GOOS=js` runtime tracks its linear-memory stack pointer in a single module-wide global (`REG_SP`) and emits `//go:wasmimport` as a raw non-yielding `call`:

1. **Concurrent re-entry corrupts `REG_SP`**: Calling a second `WebAssembly.promising` export (or triggering a `syscall/js` `js.FuncOf` callback via `wasm_exec.js` `_resume()`) while a first JSPI call is suspended **overwrites `REG_SP`** and corrupts the suspended call's stack/return values.
2. **All Go goroutines freeze while suspended**: Because `//go:wasmimport` suspends V8's stack without calling Go's `gopark()`, other goroutines in the same Wasm instance cannot run until the `WebAssembly.Suspending` Promise resolves.

### 3B. Mandatory TypeScript Bridge Wrapper
Wrap `go.importObject` imports in `new WebAssembly.Suspending(...)`, wrap exports in `WebAssembly.promising(...)`, and serialize every call through a FIFO async mutex (`enqueue`):

```typescript
interface WasmMemExports extends WebAssembly.Exports {
	mem: WebAssembly.Memory;
	getScratchHeader: () => number;
	allocBuffer: (size: number, outHeaderPtr: number) => number;
	processVaultNote: (pathBufID: number) => Promise<number> | number;
}

export class JspiWasmRunner {
	private queue: Promise<unknown> = Promise.resolve();
	private readonly encoder = new TextEncoder();
	private readonly decoder = new TextDecoder();

	/** Serializes all entries into the Wasm instance so REG_SP is never clobbered. */
	private enqueue<T>(fn: () => Promise<T>): Promise<T> {
		const next = this.queue.then(fn, fn);
		this.queue = next.then(() => undefined, () => undefined);
		return next;
	}

	// Build imports before WebAssembly.instantiate(wasmBytes, go.importObject):
	buildHostImports(getExports: () => WasmMemExports, app: App) {
		return {
			vaultRead: new WebAssembly.Suspending(
				async (pathPtr: number, pathLen: number, outHeaderPtr: number): Promise<number> => {
					const exports = getExports();
					// Re-read exports.mem.buffer after any await or memory growth!
					const path = this.decoder.decode(
						new Uint8Array(exports.mem.buffer, pathPtr, pathLen),
					);
					const file = app.vault.getFileByPath(path);
					if (!file) return -1;

					const bytes = this.encoder.encode(await app.vault.read(file));
					const bufId = exports.allocBuffer(bytes.byteLength, outHeaderPtr);
					if (bytes.byteLength > 0) {
						const dstPtr = new DataView(exports.mem.buffer).getUint32(outHeaderPtr, true);
						new Uint8Array(exports.mem.buffer, dstPtr, bytes.byteLength).set(bytes);
					}
					return bufId;
				},
			),
		};
	}

	runProcessNote(exports: WasmMemExports, path: string): Promise<number> {
		const promisingProcess = WebAssembly.promising(
			exports.processVaultNote as unknown as (pathBufID: number) => number,
		);
		return this.enqueue(async () => {
			const bytes = this.encoder.encode(path);
			const hdrPtr = exports.getScratchHeader();
			const bufId = exports.allocBuffer(bytes.byteLength, hdrPtr);
			if (bytes.byteLength > 0) {
				const ptr = new DataView(exports.mem.buffer).getUint32(hdrPtr, true);
				new Uint8Array(exports.mem.buffer, ptr, bytes.byteLength).set(bytes);
			}
			return await promisingProcess(bufId);
		});
	}
}
```

> **Important memory safety rule**: Never cache `new Uint8Array(exports.mem.buffer)` across an `await` or an `allocBuffer(...)` call—Go memory growth detaches the old `ArrayBuffer`. Always re-read `exports.mem.buffer` after returning from any allocation or `await`.

---

## 4. Handling Parallel I/O & Future Runtime Roadmap

### How to Parallelize I/O Under Today's Non-Reentrant Restriction
- **Do not** launch 4 Go goroutines calling a `WebAssembly.Suspending` `httpFetch`—they will freeze each other and run sequentially.
- **Instead, choose one**:
  1. **JS-side batching**: Pass an array/batch of requests into a single `WebAssembly.Suspending` import that runs `await Promise.all(...)` in TypeScript/V8 and returns all results at once.
  2. **Hybrid**: Keep parallel HTTP fan-out (like `gdrive.Transport`) on `Promise.then` + Go channels (`syscall/js`), and use JSPI only for high-frequency local `Vault` / `IndexedDB` loops.

### Upstream Roadmap (When the Mutex Can Be Retired)
The non-reentrancy restriction is a temporary Go runtime limitation, not a JSPI limitation. Track these upstream issues before removing the TypeScript FIFO mutex or `syscall/js` workarounds:
- **`golang/go#81963`** (`runtime: stack switching on wasm` / `GOWASM=stackswitch`) — hooking Go's goroutine scheduler directly into native Wasm stack switching.
- **`golang/go#77141`** (`proposal: all: add wasip3/wasm port`) — async component-model / stack-switching scheduler support.
- **`tinygo-org/tinygo#4789`** (`JSPI scheduler`) — TinyGo's native JSPI goroutine scheduler alternative to `-scheduler=asyncify`.
