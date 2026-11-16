import { afterEach, describe, expect, it, vi } from 'vitest';
import type { App } from 'obsidian';
import type { VaultFileStat } from '../settings.generated';
import type { VaultBackend } from '../vault/sync-adapter';

vi.mock('../../build/wasm_exec.js', () => ({}));
vi.mock('../../build/plugin.wasm', () => ({ default: new Uint8Array([0, 97, 115, 109]) }));

import { GoWasmBridge } from './bridge';
import {
	buildObsidianHostImports,
	takeLastJspiError,
	takeMergeDirectResult,
	writeStringToGo,
} from './jspi-host';
import type {
	WasmBridgeApi,
	WasmJspiExports,
	WasmWindowGlobals,
	WebAssemblyWithJspi,
} from './types';

class MemoryVaultBackend implements VaultBackend {
	readonly files = new Map<string, string>([['sync/a.md', 'hello world']]);

	listFlatStats(folderPath: string): VaultFileStat[] {
		const prefix = folderPath ? `${folderPath}/` : '';
		const stats: VaultFileStat[] = [];
		for (const [p, body] of this.files.entries()) {
			if (p.startsWith(prefix) && !p.slice(prefix.length).includes('/')) {
				stats.push({
					name: p.slice(prefix.length),
					path: p,
					size: body.length,
					mtimeMs: 100,
				});
			}
		}
		return stats;
	}

	readText(filePath: string): Promise<string | null> {
		return Promise.resolve(this.files.get(filePath) ?? null);
	}

	writeText(filePath: string, content: string): Promise<void> {
		this.files.set(filePath, content);
		return Promise.resolve();
	}

	renameFile(oldPath: string, newPath: string): Promise<void> {
		const existing = this.files.get(oldPath);
		if (existing !== undefined) {
			this.files.delete(oldPath);
			this.files.set(newPath, existing);
		}
		return Promise.resolve();
	}

	exists(filePath: string): Promise<boolean> {
		return Promise.resolve(this.files.has(filePath));
	}

	ensureFolder(): Promise<void> {
		return Promise.resolve();
	}
}

function makeMockJspiExports(): {
	exports: WasmJspiExports;
	scratchPtr: number;
	mergePtr: number;
} {
	const memory = new WebAssembly.Memory({ initial: 1 });
	const scratchPtr = 64;
	const mergePtr = 128;
	let nextOffset = 256;
	let nextId = 1;
	const pinned = new Map<number, { ptr: number; len: number }>();
	const encoder = new TextEncoder();
	const decoder = new TextDecoder();

	const allocBuffer = (size: number, outHeaderPtr: number): number => {
		const ptr = nextOffset;
		nextOffset += Math.max(size, 8);
		const view = new DataView(memory.buffer);
		view.setUint32(outHeaderPtr, ptr, true);
		view.setUint32(outHeaderPtr + 4, size, true);
		if (size === 0) {
			return 0;
		}
		const id = nextId++;
		pinned.set(id, { ptr, len: size });
		return id;
	};

	const readPinned = (id: number): string => {
		const entry = pinned.get(id);
		if (!entry) return '';
		pinned.delete(id);
		return decoder.decode(new Uint8Array(memory.buffer, entry.ptr, entry.len));
	};

	const exports = {
		mem: memory,
		getScratchHeader: () => scratchPtr,
		getMergeDirectHeader: () => mergePtr,
		allocBuffer,
		releaseBuffer: (id: number) => {
			pinned.delete(id);
		},
		getLastJspiError: (outHdr: number) => {
			const bytes = encoder.encode('mock error');
			const id = allocBuffer(bytes.byteLength, outHdr);
			new Uint8Array(
				memory.buffer,
				new DataView(memory.buffer).getUint32(outHdr, true),
				bytes.byteLength,
			).set(bytes);
			return id;
		},
		applyPullBatch: () => 0,
		preparePushInputs: () => 1,
		applyPreparedPush: () => 0,
		collectVaultStats: (syncId: number) => (readPinned(syncId) ? 42 : 0),
		readVaultFilesForScan: () => 0,
		reconcileTextDirect: (
			baseId: number,
			localId: number,
			remoteId: number,
			outMergePtr: number,
		) => {
			const baseText = readPinned(baseId);
			const localText = readPinned(localId);
			const remoteText = readPinned(remoteId);
			const merged = `${baseText}|${localText}|${remoteText}`;
			const bytes = encoder.encode(merged);
			const tmpHdr = 96;
			const bufId = allocBuffer(bytes.byteLength, tmpHdr);
			const view = new DataView(memory.buffer);
			const ptr = view.getUint32(tmpHdr, true);
			new Uint8Array(memory.buffer, ptr, bytes.byteLength).set(bytes);
			view.setInt32(outMergePtr, 0, true);
			view.setUint32(outMergePtr + 4, bufId, true);
			view.setUint32(outMergePtr + 8, ptr, true);
			view.setUint32(outMergePtr + 12, bytes.byteLength, true);
			return 0;
		},
	} as unknown as WasmJspiExports;

	return { exports, scratchPtr, mergePtr };
}

describe('jspi-host linear-memory helpers & host imports', () => {
	it('writes and reads UTF-8 buffers and host vault calls safely', async () => {
		const { exports, scratchPtr, mergePtr } = makeMockJspiExports();
		const vault = new MemoryVaultBackend();

		class FakeSuspending {
			readonly fn: (...args: unknown[]) => unknown;
			constructor(fn: (...args: unknown[]) => unknown) {
				this.fn = fn;
			}
		}

		const imports = buildObsidianHostImports(
			() => exports,
			vault,
			{
				Suspending: FakeSuspending as unknown as WebAssemblyWithJspi['Suspending'],
				promising: ((fn) => fn as never),
				instantiate: WebAssembly.instantiate,
			},
		);

		const pathId = writeStringToGo(exports, 'sync/a.md', scratchPtr);
		const view = new DataView(exports.mem.buffer);
		const pathPtr = view.getUint32(scratchPtr, true);
		const pathLen = view.getUint32(scratchPtr + 4, true);

		const readWrapper = imports.vaultRead as unknown as FakeSuspending;
		const readBufId = await (readWrapper.fn(pathPtr, pathLen, scratchPtr) as Promise<number>);
		expect(readBufId).toBeGreaterThan(0);
		exports.releaseBuffer(pathId);
		exports.releaseBuffer(readBufId);

		const baseId = writeStringToGo(exports, 'b', scratchPtr);
		const localId = writeStringToGo(exports, 'l', scratchPtr);
		const remoteId = writeStringToGo(exports, 'r', scratchPtr);
		exports.reconcileTextDirect(baseId, localId, remoteId, mergePtr);
		expect(takeMergeDirectResult(exports, mergePtr)).toEqual({
			outcome: 'clean',
			content: 'b|l|r',
		});
		expect(takeLastJspiError(exports, scratchPtr, 'fallback')).toBe('mock error');
	});
});

describe('GoWasmBridge single-root + JSPI + IDB mutex', () => {
	afterEach(() => {
		vi.unstubAllGlobals();
		vi.restoreAllMocks();
	});

	it('injects single-root host, scans sync state, and serializes concurrent calls', async () => {
		vi.spyOn(console, 'warn').mockImplementation(() => {});
		const { exports } = makeMockJspiExports();
		const fakeGlobals: WasmWindowGlobals & { setTimeout: typeof setTimeout } = {
			setTimeout,
		};

		const order: string[] = [];
		const shutdownMock = vi.fn();
		const fakeApi: WasmBridgeApi = {
			invoke: vi.fn(async (method: string) => {
				order.push(`start:${method}`);
				await new Promise<void>((resolve) => setTimeout(resolve, 10));
				order.push(`end:${method}`);
				if (method === 'listFiles') {
					return JSON.stringify({
						folderId: 'folder-1',
						files: [
							{
								id: 'f1',
								name: 'note.md',
								mimeType: 'text/markdown',
								modifiedTime: '2026-11-15T00:00:00Z',
								size: '10',
								sha256Checksum: 'abc',
								md5Checksum: 'def',
							},
						],
						rotatedRefreshToken: 'rotated-secret-99',
					});
				}
				if (method === 'prepareVaultScan') {
					return JSON.stringify({ scanId: 42, pathsToRead: ['sync/a.md'] });
				}
				if (method === 'commitSyncScan') {
					return JSON.stringify({
						records: [],
						renamedVaultFiles: 0,
						renamedDriveFiles: 0,
						hashedVaultFiles: 1,
						rotatedRefreshToken: '',
					});
				}
				return JSON.stringify({ configured: true, maxPendingRequests: 3 });
			}),
			shutdown: shutdownMock,
		};

		class FakeGoRuntime {
			importObject: WebAssembly.Imports = {};
			run(): Promise<void> {
				fakeGlobals.__testgdoc = fakeApi;
				return Promise.resolve();
			}
		}
		fakeGlobals.Go = FakeGoRuntime;

		const fakeApp = {
			vault: { getName: () => 'TestVault' },
		} as unknown as App;
		const mockVault = new MemoryVaultBackend();

		vi.stubGlobal('window', fakeGlobals);
		vi.stubGlobal('WebAssembly', {
			Suspending: class {},
			promising: (fn: unknown) => fn,
			instantiate: vi.fn().mockResolvedValue({
				instance: { exports } as unknown as WebAssembly.Instance,
				module: {} as WebAssembly.Module,
			}),
		});

		const rotated: string[] = [];
		const bridge = new GoWasmBridge(
			fakeApp,
			(tok) => rotated.push(tok),
			{
				vaultBackend: mockVault,
				obsidianHost: { requestUrl: vi.fn(), Notice: vi.fn() },
			},
		);

		const [cfgRes, listRes, scanRes] = await Promise.all([
			bridge.configure({ refreshToken: 'rt', maxPendingRequests: 3 }),
			bridge.listFiles({ folderPath: '/test-gdoc' }),
			bridge.getSyncState({
				driveFolderPath: '/test-gdoc',
				localSyncFolderPath: 'sync',
				includeDrive: false,
			}),
		]);

		expect(cfgRes.configured).toBe(true);
		expect(listRes.folderId).toBe('folder-1');
		expect(scanRes.hashedVaultFiles).toBe(1);
		expect(rotated).toEqual(['rotated-secret-99']);
		expect(bridge.initDurationMs).not.toBeNull();
		expect(fakeGlobals.__testgdocHost?.app).toBe(fakeApp);
		expect(order).toEqual([
			'start:configure',
			'end:configure',
			'start:listFiles',
			'end:listFiles',
			'start:prepareVaultScan',
			'end:prepareVaultScan',
			'start:commitSyncScan',
			'end:commitSyncScan',
		]);

		bridge.unload();
		expect(shutdownMock).toHaveBeenCalledOnce();
		expect(fakeGlobals.__testgdocHost).toBeUndefined();
	});
});
