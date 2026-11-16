/**
 * WebAssembly JSPI (`WebAssembly.Suspending` / `WebAssembly.promising`) host imports and
 * linear-memory `SliceHeader` helpers for calling Obsidian `Vault` APIs from Go.
 */

import type { VaultBackend } from '../vault/sync-adapter';
import type { PromisedWasmExports, WebAssemblyWithJspi, WasmJspiExports } from './types';

const encoder = new TextEncoder();
const decoder = new TextDecoder();

/**
 * Decodes a UTF-8 slice from Go linear memory (`ptr`, `len`).
 * Always invoke synchronously before any `await` or Go allocation that could grow memory.
 */
function decodeMemoryString(exports: WasmJspiExports, ptr: number, len: number): string {
	if (len <= 0) {
		return '';
	}
	return decoder.decode(new Uint8Array(exports.mem.buffer, ptr, len));
}

/**
 * Allocates a pinned Go buffer via `allocBuffer(size, headerPtr)`, copies `text` UTF-8 bytes into
 * linear memory (re-reading `exports.mem.buffer` after allocation), and returns the `bufId`.
 */
export function writeStringToGo(
	exports: WasmJspiExports,
	text: string,
	headerPtr: number,
): number {
	const bytes = encoder.encode(text);
	const bufId = exports.allocBuffer(bytes.byteLength, headerPtr);
	if (bytes.byteLength > 0) {
		const dstPtr = new DataView(exports.mem.buffer).getUint32(headerPtr, true);
		new Uint8Array(exports.mem.buffer, dstPtr, bytes.byteLength).set(bytes);
	}
	return bufId;
}

/**
 * Reads a pinned Go buffer referenced by `headerPtr` (`SliceHeader{Ptr, Len}`) as a UTF-8 string
 * and immediately releases `bufId` back to Go GC.
 */
export function takePinnedString(
	exports: WasmJspiExports,
	bufId: number,
	headerPtr: number,
): string {
	if (bufId <= 0) {
		return '';
	}
	try {
		const view = new DataView(exports.mem.buffer);
		const ptr = view.getUint32(headerPtr, true);
		const len = view.getUint32(headerPtr + 4, true);
		return decodeMemoryString(exports, ptr, len);
	} finally {
		exports.releaseBuffer(bufId);
	}
}

/** Reads the last Go JSPI error string stored in Go's scratch header, or returns `fallback`. */
export function takeLastJspiError(
	exports: WasmJspiExports,
	scratchHeaderPtr: number,
	fallback: string,
): string {
	const errBufId = exports.getLastJspiError(scratchHeaderPtr);
	const msg = takePinnedString(exports, errBufId, scratchHeaderPtr);
	return msg || fallback;
}

/**
 * Reads the output of `reconcileTextDirect` from a `MergeDirectHeader{Outcome, BufID, Ptr, Len}`
 * and frees the pinned Go result buffer.
 */
export function takeMergeDirectResult(
	exports: WasmJspiExports,
	mergeHeaderPtr: number,
): { outcome: 'clean' | 'folded' | 'keepBoth'; content: string } {
	const view = new DataView(exports.mem.buffer);
	const outcomeCode = view.getInt32(mergeHeaderPtr, true);
	const bufId = view.getUint32(mergeHeaderPtr + 4, true);
	const ptr = view.getUint32(mergeHeaderPtr + 8, true);
	const len = view.getUint32(mergeHeaderPtr + 12, true);
	try {
		const content = decodeMemoryString(exports, ptr, len);
		const outcome =
			outcomeCode === 0 ? 'clean' : outcomeCode === 1 ? 'folded' : 'keepBoth';
		return { outcome, content };
	} finally {
		if (bufId > 0) {
			exports.releaseBuffer(bufId);
		}
	}
}

/**
 * Builds the `obsidianHost` `WebAssembly.Imports` namespace backed by `WebAssembly.Suspending`
 * for Obsidian `VaultBackend` stat, read, write, exists, and rename operations.
 */
export function buildObsidianHostImports(
	getExports: () => WasmJspiExports,
	vault: VaultBackend,
	wasmGlobal: WebAssemblyWithJspi = WebAssembly,
): Record<string, WebAssembly.ImportValue> {
	const SuspendingCtor = wasmGlobal.Suspending;
	if (!SuspendingCtor) {
		throw new Error('WebAssembly.Suspending is unavailable');
	}

	return {
		vaultListStats: new SuspendingCtor(
			(folderPtr: number, folderLen: number, outHeaderPtr: number): Promise<number> => {
				try {
					const exports = getExports();
					const folderPath = decodeMemoryString(exports, folderPtr, folderLen);
					const statsJson = JSON.stringify(vault.listFlatStats(folderPath));
					return Promise.resolve(writeStringToGo(exports, statsJson, outHeaderPtr));
				} catch {
					return Promise.resolve(-1);
				}
			},
		),
		vaultExists: new SuspendingCtor(
			async (pathPtr: number, pathLen: number): Promise<number> => {
				try {
					const exports = getExports();
					const filePath = decodeMemoryString(exports, pathPtr, pathLen);
					return (await vault.exists(filePath)) ? 1 : 0;
				} catch {
					return -1;
				}
			},
		),
		vaultRead: new SuspendingCtor(
			async (pathPtr: number, pathLen: number, outHeaderPtr: number): Promise<number> => {
				try {
					const exports = getExports();
					const filePath = decodeMemoryString(exports, pathPtr, pathLen);
					const content = await vault.readText(filePath);
					if (content === null) {
						return -2;
					}
					return writeStringToGo(exports, content, outHeaderPtr);
				} catch {
					return -1;
				}
			},
		),
		vaultWrite: new SuspendingCtor(
			async (
				pathPtr: number,
				pathLen: number,
				dataPtr: number,
				dataLen: number,
			): Promise<number> => {
				try {
					const exports = getExports();
					const filePath = decodeMemoryString(exports, pathPtr, pathLen);
					const content = decodeMemoryString(exports, dataPtr, dataLen);
					await vault.writeText(filePath, content);
					return 0;
				} catch {
					return -1;
				}
			},
		),
		vaultRename: new SuspendingCtor(
			async (
				oldPtr: number,
				oldLen: number,
				newPtr: number,
				newLen: number,
			): Promise<number> => {
				try {
					const exports = getExports();
					const oldPath = decodeMemoryString(exports, oldPtr, oldLen);
					const newPath = decodeMemoryString(exports, newPtr, newLen);
					await vault.renameFile(oldPath, newPath);
					return 0;
				} catch {
					return -1;
				}
			},
		),
	};
}

/** Wraps suspending `//go:wasmexport` functions with `WebAssembly.promising(...)`. */
export function bindPromisingExports(
	exports: WasmJspiExports,
	wasmGlobal: WebAssemblyWithJspi = WebAssembly,
): PromisedWasmExports {
	const promisingFn = wasmGlobal.promising;
	if (!promisingFn) {
		throw new Error('WebAssembly.promising is unavailable');
	}
	return {
		applyPullBatch: promisingFn(exports.applyPullBatch),
		preparePushInputs: promisingFn(exports.preparePushInputs),
		applyPreparedPush: promisingFn(exports.applyPreparedPush),
		collectVaultStats: promisingFn(exports.collectVaultStats),
		readVaultFilesForScan: promisingFn(exports.readVaultFilesForScan),
	};
}
