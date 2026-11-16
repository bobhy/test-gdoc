/**
 * Lazy-loading hybrid JSPI + single-root host + IndexedDB bridge between the Obsidian
 * TypeScript plugin and the embedded Go Wasm module.
 */

import '../../build/wasm_exec.js';
import * as obsidianModule from 'obsidian';
import type { App } from 'obsidian';
import wasmBytes from '../../build/plugin.wasm';
import type {
	BaseSnapshotResult,
	ConfigureRequest,
	ConfigureResult,
	GetSyncStateRequest,
	GetSyncStateResult,
	IDBBenchResult,
	ListFilesRequest,
	ListFilesResult,
	PullSummary,
	PushApplySummary,
	ReadFileRequest,
	ReadFileResult,
	VaultEventHint,
	WriteFileRequest,
	WriteFileResult,
} from '../settings.generated';
import { ObsidianVaultBackend, type VaultBackend } from '../vault/sync-adapter';
import {
	bindPromisingExports,
	buildObsidianHostImports,
	takeLastJspiError,
	takeMergeDirectResult,
	takePinnedString,
	writeStringToGo,
} from './jspi-host';
import { assertJspiSupported } from './platform';
import type {
	ObsidianHostModule,
	PromisedWasmExports,
	WasmBridgeApi,
	WasmJspiExports,
	WasmWindowGlobals,
} from './types';
import {
	assertBaseSnapshotResult,
	assertConfigureResult,
	assertGetSyncStateResult,
	assertIDBBenchResult,
	assertListFilesResult,
	assertPrepareScanResponse,
	assertPullPrepareResult,
	assertPullSummary,
	assertPushApplySummary,
	assertPushExecuteResult,
	assertReadFileResult,
	assertWriteFileResult,
} from './validators';

const INIT_TIMEOUT_MS = 5000;
const POLL_INTERVAL_MS = 10;

interface RuntimeState {
	api: WasmBridgeApi;
	exports: WasmJspiExports;
	promised: PromisedWasmExports;
	scratchHeaderPtr: number;
	mergeHeaderPtr: number;
}

function wasmGlobals(): WasmWindowGlobals {
	return window as unknown as WasmWindowGlobals;
}

/**
 * Callback invoked whenever Go reports a newly rotated OAuth refresh token so
 * TypeScript can persist it to `SecretStorage`.
 */
export type RotatedTokenHandler = (refreshToken: string) => void;

/** Optional host overrides for Vitest unit testing. */
export interface GoWasmBridgeOptions {
	vaultBackend?: VaultBackend;
	obsidianHost?: ObsidianHostModule;
}

/**
 * Manages the lifecycle of the Go WebAssembly runtime, serializing all `syscall/js`
 * (including `go-indexeddb`) and JSPI (`WebAssembly.promising`) entries through a FIFO
 * mutex to protect Go's `REG_SP`.
 */
export class GoWasmBridge {
	private readyPromise: Promise<RuntimeState> | null = null;
	private queue: Promise<unknown> = Promise.resolve();
	private lastInitDurationMs: number | null = null;
	private lastOpDurationMs: number | null = null;

	private readonly app: App;
	private readonly vaultBackend: VaultBackend;
	private readonly obsidianHost: ObsidianHostModule;
	private onRotatedToken?: RotatedTokenHandler;

	/** Creates a bridge instance bound to the plugin `App`; optional overrides support testing. */
	constructor(
		app: App,
		onRotatedToken?: RotatedTokenHandler,
		options?: GoWasmBridgeOptions,
	) {
		this.app = app;
		this.onRotatedToken = onRotatedToken;
		this.vaultBackend =
			options?.vaultBackend ?? new ObsidianVaultBackend(app.vault, app.fileManager);
		this.obsidianHost = options?.obsidianHost ?? {
			requestUrl: obsidianModule.requestUrl,
			Notice: obsidianModule.Notice,
		};
	}

	/** Updates the handler invoked when Go surfaces a rotated refresh token. */
	setRotatedTokenHandler(handler: RotatedTokenHandler): void {
		this.onRotatedToken = handler;
	}

	/** Returns the measured lazy Wasm initialization latency in milliseconds, or `null` before init. */
	get initDurationMs(): number | null {
		return this.lastInitDurationMs;
	}

	/** Returns the duration in milliseconds of the most recent bridge operation, or `null`. */
	get lastOperationDurationMs(): number | null {
		return this.lastOpDurationMs;
	}

	/** Configures Go-side OAuth refresh token, vault DB name, and outbound Drive concurrency limit. */
	async configure(req: Omit<ConfigureRequest, 'vaultName'> & { vaultName?: string }): Promise<ConfigureResult> {
		return await this.enqueue(async () => {
			const state = await this.ensureInitialized();
			const payload: ConfigureRequest = {
				refreshToken: req.refreshToken,
				maxPendingRequests: req.maxPendingRequests,
				vaultName: req.vaultName ?? this.app.vault.getName(),
			};
			const parsed = await invokeJson(state.api, 'configure', payload);
			assertConfigureResult(parsed);
			return parsed;
		});
	}

	/** Resolves `folderPath` on Drive and lists flat `.md`/`.txt` files inside it. */
	async listFiles(req: ListFilesRequest): Promise<ListFilesResult> {
		return await this.enqueue(async () => {
			const state = await this.ensureInitialized();
			const parsed = await invokeJson(state.api, 'listFiles', req);
			assertListFilesResult(parsed);
			this.emitRotatedToken(parsed.rotatedRefreshToken);
			return parsed;
		});
	}

	/** Downloads metadata and UTF-8 text content for a Drive file ID. */
	async readFile(req: ReadFileRequest): Promise<ReadFileResult> {
		return await this.enqueue(async () => {
			const state = await this.ensureInitialized();
			const parsed = await invokeJson(state.api, 'readFile', req);
			assertReadFileResult(parsed);
			this.emitRotatedToken(parsed.rotatedRefreshToken);
			return parsed;
		});
	}

	/** Creates or updates a flat Drive file directly (used by the Drive browser simulator). */
	async writeFile(req: WriteFileRequest): Promise<WriteFileResult> {
		return await this.enqueue(async () => {
			const state = await this.ensureInitialized();
			const parsed = await invokeJson(state.api, 'writeFile', req);
			assertWriteFileResult(parsed);
			this.emitRotatedToken(parsed.rotatedRefreshToken);
			return parsed;
		});
	}

	/** Forwards Obsidian `create`/`delete`/`modify` notifications to Go's IndexedDB store. */
	async recordVaultHints(hints: VaultEventHint[]): Promise<void> {
		if (hints.length === 0) {
			return;
		}
		await this.enqueue(async () => {
			const state = await this.ensureInitialized();
			for (const hint of hints) {
				await invokeJson(state.api, 'recordVaultEvent', hint);
			}
		});
	}

	/**
	 * Scans Vault stats via JSPI, compares against IndexedDB, reads only changed/new files via JSPI,
	 * reconciles Vault+Drive renames and relations into IndexedDB, and returns all `SyncRecord`s.
	 */
	async getSyncState(req: GetSyncStateRequest): Promise<GetSyncStateResult> {
		return await this.enqueue(async () => {
			const state = await this.ensureInitialized();
			const startedAt = performance.now();
			try {
				const syncBufId = writeStringToGo(
					state.exports,
					req.localSyncFolderPath,
					state.scratchHeaderPtr,
				);
				const scanId = await state.promised.collectVaultStats(syncBufId);
				if (scanId <= 0) {
					throw new Error(
						takeLastJspiError(state.exports, state.scratchHeaderPtr, 'Failed to stat Vault folder'),
					);
				}

				const prepParsed = await invokeJson(state.api, 'prepareVaultScan', {
					scanId,
					driveFolderPath: req.driveFolderPath,
					localSyncFolderPath: req.localSyncFolderPath,
					includeDrive: req.includeDrive,
				});
				assertPrepareScanResponse(prepParsed);

				if (prepParsed.pathsToRead.length > 0) {
					const rc = await state.promised.readVaultFilesForScan(scanId);
					if (rc !== 0) {
						throw new Error(
							takeLastJspiError(
								state.exports,
								state.scratchHeaderPtr,
								'Failed to read changed Vault files',
							),
						);
					}
				}

				const commitParsed = await invokeJson(state.api, 'commitSyncScan', { scanId });
				assertGetSyncStateResult(commitParsed);
				this.emitRotatedToken(commitParsed.rotatedRefreshToken);
				return commitParsed;
			} finally {
				this.lastOpDurationMs = Math.round((performance.now() - startedAt) * 100) / 100;
			}
		});
	}

	/**
	 * Downloads flat files from `driveFolder` in parallel inside Go, updates IndexedDB metadata
	 * and common bases, and writes/renames files in `syncFolder` via JSPI.
	 */
	async pullDriveFolder(driveFolder: string, syncFolder: string): Promise<PullSummary> {
		return await this.enqueue(async () => {
			const state = await this.ensureInitialized();
			const startedAt = performance.now();
			try {
				const prepParsed = await invokeJson(state.api, 'pullPrepare', {
					folderPath: driveFolder,
				});
				assertPullPrepareResult(prepParsed);
				this.emitRotatedToken(prepParsed.rotatedRefreshToken);

				const syncBufId = writeStringToGo(state.exports, syncFolder, state.scratchHeaderPtr);
				const outBufId = await state.promised.applyPullBatch(
					prepParsed.batchId,
					syncBufId,
					state.scratchHeaderPtr,
				);
				if (outBufId <= 0) {
					throw new Error(
						takeLastJspiError(state.exports, state.scratchHeaderPtr, 'Failed to apply pull batch'),
					);
				}
				const sumRaw = takePinnedString(state.exports, outBufId, state.scratchHeaderPtr);
				const sumParsed = JSON.parse(sumRaw) as unknown;
				assertPullSummary(sumParsed);
				return sumParsed;
			} finally {
				this.lastOpDurationMs = Math.round((performance.now() - startedAt) * 100) / 100;
			}
		});
	}

	/**
	 * Reads the active local note via JSPI into Go memory, loads the stored common base from
	 * IndexedDB, runs 3-way merge + Drive update/rename in Go, and updates local Vault via JSPI.
	 */
	async pushActiveNote(
		driveFolder: string,
		syncFolder: string,
		fileName: string,
	): Promise<PushApplySummary> {
		return await this.enqueue(async () => {
			const state = await this.ensureInitialized();
			const startedAt = performance.now();
			try {
				const syncBufId = writeStringToGo(state.exports, syncFolder, state.scratchHeaderPtr);
				const nameBufId = writeStringToGo(state.exports, fileName, state.scratchHeaderPtr);

				const batchId = await state.promised.preparePushInputs(syncBufId, nameBufId);
				if (batchId <= 0) {
					throw new Error(
						takeLastJspiError(
							state.exports,
							state.scratchHeaderPtr,
							'Failed to prepare push inputs',
						),
					);
				}

				const execParsed = await invokeJson(state.api, 'executePreparedPush', {
					folderPath: driveFolder,
					batchId,
				});
				assertPushExecuteResult(execParsed);
				this.emitRotatedToken(execParsed.rotatedRefreshToken);

				const outBufId = await state.promised.applyPreparedPush(
					batchId,
					state.scratchHeaderPtr,
				);
				if (outBufId <= 0) {
					throw new Error(
						takeLastJspiError(
							state.exports,
							state.scratchHeaderPtr,
							'Failed to apply push outcome',
						),
					);
				}
				const sumRaw = takePinnedString(state.exports, outBufId, state.scratchHeaderPtr);
				const sumParsed = JSON.parse(sumRaw) as unknown;
				assertPushApplySummary(sumParsed);
				return sumParsed;
			} finally {
				this.lastOpDurationMs = Math.round((performance.now() - startedAt) * 100) / 100;
			}
		});
	}

	/** Reads the stored common base snapshot (if present) for `fileName` from IndexedDB. */
	async getBaseSnapshot(fileName: string): Promise<BaseSnapshotResult> {
		return await this.enqueue(async () => {
			const state = await this.ensureInitialized();
			const parsed = await invokeJson(state.api, 'getBaseSnapshot', { fileName });
			assertBaseSnapshotResult(parsed);
			return parsed;
		});
	}

	/** Writes or updates the stored common base snapshot for `fileName` in IndexedDB. */
	async setBaseSnapshot(fileName: string, content: string): Promise<BaseSnapshotResult> {
		return await this.enqueue(async () => {
			const state = await this.ensureInitialized();
			const parsed = await invokeJson(state.api, 'setBaseSnapshot', { fileName, content });
			assertBaseSnapshotResult(parsed);
			return parsed;
		});
	}

	/** Removes any stored common base snapshot for `fileName` from IndexedDB. */
	async clearBaseSnapshot(fileName: string): Promise<BaseSnapshotResult> {
		return await this.enqueue(async () => {
			const state = await this.ensureInitialized();
			const parsed = await invokeJson(state.api, 'clearBaseSnapshot', { fileName });
			assertBaseSnapshotResult(parsed);
			return parsed;
		});
	}

	/** Executes an IndexedDB write/scan/get/clear benchmark directly via `hack-pad/go-indexeddb`. */
	async runIdbBenchmark(count = 100): Promise<IDBBenchResult> {
		return await this.enqueue(async () => {
			const state = await this.ensureInitialized();
			const parsed = await invokeJson(state.api, 'runIdbBenchmark', { count });
			assertIDBBenchResult(parsed);
			return parsed;
		});
	}

	/** Invokes synchronous `reconcileTextDirect` (`//go:wasmexport`) using linear-memory buffers. */
	async reconcileTextDirect(
		baseText: string,
		localText: string,
		remoteText: string,
	): Promise<{ outcome: 'clean' | 'folded' | 'keepBoth'; content: string }> {
		return await this.enqueue(async () => {
			const state = await this.ensureInitialized();
			const baseId = writeStringToGo(state.exports, baseText, state.scratchHeaderPtr);
			const localId = writeStringToGo(state.exports, localText, state.scratchHeaderPtr);
			const remoteId = writeStringToGo(state.exports, remoteText, state.scratchHeaderPtr);
			state.exports.reconcileTextDirect(baseId, localId, remoteId, state.mergeHeaderPtr);
			return takeMergeDirectResult(state.exports, state.mergeHeaderPtr);
		});
	}

	/** Shuts down the Go Wasm runtime and removes registered global callbacks. */
	unload(): void {
		const globals = wasmGlobals();
		globals.__testgdoc?.shutdown();
		delete globals.__testgdocHost;
		this.readyPromise = null;
	}

	private enqueue<T>(fn: () => Promise<T>): Promise<T> {
		const next = this.queue.then(fn, fn);
		this.queue = next.then(
			() => undefined,
			() => undefined,
		);
		return next;
	}

	private emitRotatedToken(token: string): void {
		if (token && this.onRotatedToken) {
			this.onRotatedToken(token);
		}
	}

	private ensureInitialized(): Promise<RuntimeState> {
		if (!this.readyPromise) {
			this.readyPromise = this.init().catch((err: unknown) => {
				this.readyPromise = null;
				throw err;
			});
		}
		return this.readyPromise;
	}

	private async init(): Promise<RuntimeState> {
		assertJspiSupported();

		const startedAt = performance.now();
		const globals = wasmGlobals();
		const GoCtor = globals.Go;
		if (!GoCtor) {
			throw new Error('Go wasm_exec.js runtime is not loaded');
		}

		globals.__testgdocHost = {
			app: this.app,
			obsidian: this.obsidianHost,
		};

		const go = new GoCtor();
		let exportsRef!: WasmJspiExports;
		go.importObject.obsidianHost = buildObsidianHostImports(
			() => exportsRef,
			this.vaultBackend,
		);

		const result = await WebAssembly.instantiate(
			wasmBytes as BufferSource,
			go.importObject,
		);
		exportsRef = result.instance.exports as unknown as WasmJspiExports;
		const promised = bindPromisingExports(exportsRef);

		void go.run(result.instance).catch((err: unknown) => {
			console.error('Go Wasm runtime exited with error:', err);
		});

		const api = await waitForBridge(INIT_TIMEOUT_MS);
		const scratchHeaderPtr = exportsRef.getScratchHeader();
		const mergeHeaderPtr = exportsRef.getMergeDirectHeader();

		this.lastInitDurationMs = Math.round((performance.now() - startedAt) * 100) / 100;
		console.warn(`[test-gdoc] Go Wasm initialized in ${this.lastInitDurationMs} ms`);

		return {
			api,
			exports: exportsRef,
			promised,
			scratchHeaderPtr,
			mergeHeaderPtr,
		};
	}
}

async function invokeJson(api: WasmBridgeApi, method: string, payload: unknown): Promise<unknown> {
	const rawJson = await api.invoke(method, JSON.stringify(payload));
	return JSON.parse(rawJson) as unknown;
}

async function waitForBridge(timeoutMs: number): Promise<WasmBridgeApi> {
	const deadline = Date.now() + timeoutMs;
	while (Date.now() < deadline) {
		const api = wasmGlobals().__testgdoc;
		if (api) {
			return api;
		}
		await new Promise<void>((resolve) => {
			window.setTimeout(resolve, POLL_INTERVAL_MS);
		});
	}
	throw new Error('Timed out waiting for Go Wasm bridge initialization');
}
