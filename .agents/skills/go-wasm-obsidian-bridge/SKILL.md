---
name: go-wasm-obsidian-bridge
description: >-
  Draft workflow and code patterns for bridging Go Wasm (GOOS=js GOARCH=wasm) with
  Obsidian TypeScript APIs without per-method JSON fat thunks or Web Workers. Use
  when designing or refactoring Go↔Obsidian data models, async Promise wrappers,
  opaque Obsidian js.Value handles, bulk Uint8Array transfers, or pure-Go functional cores.
---

# Go/Wasm ↔ Obsidian Bridge Patterns (Draft)

Use this skill when extending the `test-gdoc` Wasm bridge or adapting it into a general Go + Obsidian plugin paradigm. It covers the four non-worker community patterns validated for Go 1.27 + Obsidian.

> For JSPI (`WebAssembly.Suspending` / `WebAssembly.promising`) on Chromium 137+ / Obsidian Desktop 1.10+ and Android, see the [`go-wasm-jspi`](../go-wasm-jspi/SKILL.md) skill instead.

---

## Pattern 1: Go-First Data Schemas vs. Opaque `js.Value` Handles

Never map TypeScript data objects (`TFile`, `CachedMetadata`) into Go structs by calling `val.Get("field")` repeatedly—every string property read crosses the Wasm↔JS boundary ~5 times and allocates a GC finalizer.

### 1A. Data-Transfer Structs (Go → TS Codegen)
Keep DTOs and settings in pure Go and generate TypeScript definitions:
1. Define structs with `json:"..."` tags and defaults in `go/settings/settings.go`.
2. Register new types in `go/cmd/gents/main.go`.
3. Regenerate `src/settings.generated.ts` and `build/plugin.wasm`:
   ```sh
   npm run build:wasm
   ```
4. Never edit `src/settings.generated.ts` manually.

### 1B. Opaque Service / Resource Handles (TS → Go Wrapper)
When Go drives an Obsidian object (`App`, `Vault`, `Workspace`, `TFile`, `SecretStorage`), wrap its `js.Value` as an opaque handle without unpacking fields:

```go
//go:build js && wasm

package obsbridge

import "syscall/js"

// Vault wraps an opaque Obsidian Vault js.Value handle.
type Vault struct {
	val js.Value
}

// TFile wraps an opaque Obsidian TFile js.Value handle (do not unpack fields unless needed).
type TFile struct {
	val js.Value
}

func (v Vault) GetFileByPath(path string) (TFile, bool) {
	f := v.val.Call("getFileByPath", path)
	if f.IsNull() || f.IsUndefined() {
		return TFile{}, false
	}
	return TFile{val: f}, true
}
```

---

## Pattern 2: Single-Root Host Injection + Generic `Await` Helper

Avoid writing a new TypeScript host adapter function for every Obsidian API. Inject `this.app` (and optionally the `obsidian` module for `Notice`) **once** during bridge initialization, then drive async JS Promises from Go with one reusable `Await` helper.

### 2A. TypeScript Host Injection (Once on Init)
In `src/wasm/bridge.ts` (`init()`), pass `app` and `Notice` on your host global (or via a `configureHost` init export) before calling Go RPCs:

```typescript
import * as obsidian from 'obsidian';

// Inside GoWasmBridge.init(), when setting globals.__testgdocHost:
globals.__testgdocHost = {
	app: this.app, // injected into GoWasmBridge constructor from Plugin.onload()
	obsidian,      // allows Go to construct `new host.obsidian.Notice(msg)`
	httpFetch: (req) => executeHttpFetch(req, this.requester),
};
```

> Never read `window.app` from Go—Obsidian plugin guidelines forbid relying on the global `window.app` debug handle. Always pass `this.app` explicitly from your `Plugin` instance.

### 2B. Generic `Await(ctx, promise)` in Go
Factor the `resolveFn`/`rejectFn` + `sync.Once` pattern (currently specialized in `go/cmd/plugin/roundtripper.go`) into a shared helper:

```go
//go:build js && wasm

package jsutil

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"syscall/js"
)

// Await parks the calling goroutine until promise settles or ctx is canceled.
// Always releases both js.Func callbacks via sync.Once to prevent _values leaks.
func Await(ctx context.Context, promise js.Value) (js.Value, error) {
	if promise.IsUndefined() || promise.IsNull() {
		return js.Undefined(), errors.New("jsutil.Await: expected Promise, got null/undefined")
	}

	resCh := make(chan js.Value, 1)
	errCh := make(chan error, 1)

	var resolveFn, rejectFn js.Func
	var once sync.Once
	release := func() {
		once.Do(func() {
			resolveFn.Release()
			rejectFn.Release()
		})
	}

	resolveFn = js.FuncOf(func(_ js.Value, args []js.Value) any {
		defer release()
		if len(args) > 0 {
			resCh <- args[0]
		} else {
			resCh <- js.Undefined()
		}
		return nil
	})

	rejectFn = js.FuncOf(func(_ js.Value, args []js.Value) any {
		defer release()
		msg := "JavaScript promise rejected"
		if len(args) > 0 && !args[0].IsUndefined() && !args[0].IsNull() {
			if m := args[0].Get("message"); !m.IsUndefined() {
				msg = m.String()
			} else {
				msg = fmt.Sprint(args[0])
			}
		}
		errCh <- errors.New(msg)
		return nil
	})

	promise.Call("then", resolveFn, rejectFn)

	select {
	case <-ctx.Done():
		return js.Undefined(), ctx.Err()
	case err := <-errCh:
		return js.Undefined(), err
	case val := <-resCh:
		return val, nil
	}
}
```

> **Caveat on `ctx` cancellation**: If `ctx` cancels before the Promise settles, `resolveFn`/`rejectFn` remain allocated until the JS Promise settles and runs `defer release()`. Never call `release()` before the Promise settles, or JS will log `"call to released function"` when it resolves.

### 2C. Direct Obsidian & `IndexedDB` Usage from Go
- **Obsidian `Vault.read` / `SecretStorage`**:
  ```go
  func (v Vault) Read(ctx context.Context, file TFile) (string, error) {
  	val, err := jsutil.Await(ctx, v.val.Call("read", file.val))
  	if err != nil {
  		return "", err
  	}
  	return val.String(), nil
  }
  ```
- **Browser `IndexedDB`**: `js.Global().Get("indexedDB")` is directly accessible without TS glue. Prefer **`github.com/hack-pad/go-indexeddb`** or **`github.com/hack-pad/hackpadfs/indexeddb`** (full `io/fs.FS` over IndexedDB) and **`github.com/hack-pad/safejs`** when panic-free `syscall/js` error returns are desired.
- **Cache hot `js.Value` handles**: Fetch `host.Get("app")`, `app.Get("vault")`, `js.Global().Get("Uint8Array")`, etc. once and reuse them rather than calling `js.Global().Get(...)` inside loops (prevents `_values` table churn).

---

## Pattern 3: Bulk Binary Memory Transfer (`Uint8Array` / `//go:wasmexport`)

When transferring note bodies, binary attachments, or large datasets:

1. **Avoid JSON strings for binary/large payloads**: Base64-in-JSON inflates payload size by ~33% and forces `TextEncoder`/`TextDecoder` + Go `encoding/json` reflection.
2. **Use `Uint8Array` bulk copies** (`js.CopyBytesToJS` / `js.CopyBytesToGo`), following `go/cmd/plugin/roundtripper.go`:
   - **Go → JS**:
     ```go
     u8 := uint8ArrayCtor.New(len(data))
     js.CopyBytesToJS(u8, data)
     ```
   - **JS → Go**:
     ```go
     n := u8.Get("byteLength").Int()
     buf := make([]byte, n)
     js.CopyBytesToGo(buf, u8)
     ```
3. **Synchronous compute hot paths (`//go:wasmexport`)**: For pure CPU functions that do not await JS Promises (e.g., diff3 merge, hashing, markdown AST parsing), export directly with `//go:wasmexport` (Go 1.24+) and pass pointers/lengths in `instance.exports.mem.buffer` to skip `wasm_exec.js`'s `_pendingEvent` dispatch overhead.

---

## Pattern 4: Pure-Go Functional Core / TypeScript Imperative Shell

Structure plugins so Go logic stays decoupled from `syscall/js`:

```text
go/<domain>/          ← Pure Go (NO syscall/js import!). Tested via `go test ./go/...`.
go/cmd/plugin/        ← Thin `//go:build js && wasm` adapter (bridges Go interfaces ↔ Obsidian/JS).
src/<feature>/        ← Thin TypeScript shell (Plugin lifecycle, Modal/ItemView classes, Vault batching).
```

### Checklist Before Adding a New Feature
1. Can the domain logic live in a pure Go package behind a small Go interface (like `gdrive.DoFunc` in `go/gdrive/`)? If yes, put it in `go/<domain>/` and write `*_test.go` unit tests first.
2. Does the UI require extending an Obsidian class (`Modal`, `ItemView`, `PluginSettingTab`)? Keep the class shell in `src/` and have it call Go for state/actions.
3. Is Go making 1–3 coarse calls per user action (e.g., `mergeAndPushFile`)? Keep Go-first DTOs + JSON RPC.
4. Is Go making many sequential calls into `Vault` or `IndexedDB`? Either batch on the TS side (`SyncAdapter`) or use Pattern 2 (`Await` + opaque handles) / `go-wasm-jspi`.
5. Run all project verification gates after changes:
   ```sh
   npm run test:go && npm run build && npm run lint
   ```
