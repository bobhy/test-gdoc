/**
 * Lazy-loading bridge between the Obsidian TypeScript plugin and the embedded Go Wasm module.
 */

import '../../build/wasm_exec.js';
import wasmBytes from '../../build/plugin.wasm';
import type {
	ConfigureRequest,
	ConfigureResult,
	FileMeta,
	ListFilesRequest,
	ListFilesResult,
	MergeAndPushRequest,
	MergeAndPushResult,
	ReadFileRequest,
	ReadFileResult,
	WriteFileRequest,
	WriteFileResult,
} from '../settings.generated';
import { executeHttpFetch, type RequestUrlFn } from './http-host';
import type { WasmBridgeApi, WasmWindowGlobals } from './types';

const INIT_TIMEOUT_MS = 5000;
const POLL_INTERVAL_MS = 10;

function wasmGlobals(): WasmWindowGlobals {
	return window as unknown as WasmWindowGlobals;
}

/**
 * Callback invoked whenever Go reports a newly rotated OAuth refresh token so
 * TypeScript can persist it to `SecretStorage`.
 */
export type RotatedTokenHandler = (refreshToken: string) => void;

/**
 * Manages the lifecycle of the Go WebAssembly runtime and marshals async RPC calls
 * between Obsidian TypeScript code and Go.
 */
export class GoWasmBridge {
	private readyPromise: Promise<WasmBridgeApi> | null = null;
	private lastInitDurationMs: number | null = null;
	private onRotatedToken?: RotatedTokenHandler;
	private readonly requester?: RequestUrlFn;

	/** Creates a bridge instance; optional overrides support unit testing. */
	constructor(onRotatedToken?: RotatedTokenHandler, requester?: RequestUrlFn) {
		this.onRotatedToken = onRotatedToken;
		this.requester = requester;
	}

	/** Updates the handler invoked when Go surfaces a rotated refresh token. */
	setRotatedTokenHandler(handler: RotatedTokenHandler): void {
		this.onRotatedToken = handler;
	}

	/** Returns the measured lazy Wasm initialization latency in milliseconds, or `null` if not yet initialized. */
	get initDurationMs(): number | null {
		return this.lastInitDurationMs;
	}

	/** Configures Go-side OAuth refresh token and outbound Drive concurrency limit. */
	async configure(req: ConfigureRequest): Promise<ConfigureResult> {
		const parsed = await this.call('configure', req);
		assertConfigureResult(parsed);
		return parsed;
	}

	/** Resolves `folderPath` on Drive and lists flat `.md`/`.txt` files inside it. */
	async listFiles(req: ListFilesRequest): Promise<ListFilesResult> {
		const parsed = await this.call('listFiles', req);
		assertListFilesResult(parsed);
		this.emitRotatedToken(parsed.rotatedRefreshToken);
		return parsed;
	}

	/** Downloads metadata and UTF-8 text content for a Drive file ID. */
	async readFile(req: ReadFileRequest): Promise<ReadFileResult> {
		const parsed = await this.call('readFile', req);
		assertReadFileResult(parsed);
		this.emitRotatedToken(parsed.rotatedRefreshToken);
		return parsed;
	}

	/** Creates or updates a flat Drive file directly (used by the Drive browser simulator). */
	async writeFile(req: WriteFileRequest): Promise<WriteFileResult> {
		const parsed = await this.call('writeFile', req);
		assertWriteFileResult(parsed);
		this.emitRotatedToken(parsed.rotatedRefreshToken);
		return parsed;
	}

	/** Reconciles local, base, and remote Drive content in Go and pushes updates when needed. */
	async mergeAndPushFile(req: MergeAndPushRequest): Promise<MergeAndPushResult> {
		const parsed = await this.call('mergeAndPushFile', req);
		assertMergeAndPushResult(parsed);
		this.emitRotatedToken(parsed.rotatedRefreshToken);
		return parsed;
	}

	/** Shuts down the Go Wasm runtime and removes registered global callbacks. */
	unload(): void {
		const globals = wasmGlobals();
		globals.__testgdoc?.shutdown();
		delete globals.__testgdocHost;
		this.readyPromise = null;
	}

	private emitRotatedToken(token: string): void {
		if (token && this.onRotatedToken) {
			this.onRotatedToken(token);
		}
	}

	private async call(method: string, payload: unknown): Promise<unknown> {
		const api = await this.ensureInitialized();
		const rawJson = await api.invoke(method, JSON.stringify(payload));
		return JSON.parse(rawJson) as unknown;
	}

	private ensureInitialized(): Promise<WasmBridgeApi> {
		if (!this.readyPromise) {
			this.readyPromise = this.init().catch((err: unknown) => {
				this.readyPromise = null;
				throw err;
			});
		}
		return this.readyPromise;
	}

	private async init(): Promise<WasmBridgeApi> {
		const startedAt = performance.now();
		const globals = wasmGlobals();
		const GoCtor = globals.Go;
		if (!GoCtor) {
			throw new Error('Go wasm_exec.js runtime is not loaded');
		}

		globals.__testgdocHost = {
			httpFetch: (req) => executeHttpFetch(req, this.requester),
		};

		const go = new GoCtor();
		const result = await WebAssembly.instantiate(
			wasmBytes as BufferSource,
			go.importObject,
		);

		void go.run(result.instance).catch((err: unknown) => {
			console.error('Go Wasm runtime exited with error:', err);
		});

		const api = await waitForBridge(INIT_TIMEOUT_MS);
		this.lastInitDurationMs = Math.round((performance.now() - startedAt) * 100) / 100;
		console.warn(`[test-gdoc] Go Wasm initialized in ${this.lastInitDurationMs} ms`);
		return api;
	}
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

function asRecord(value: unknown, label: string): Record<string, unknown> {
	if (typeof value !== 'object' || value === null) {
		throw new Error(`Go Wasm returned a non-object ${label}`);
	}
	return value as Record<string, unknown>;
}

function assertFileMeta(value: unknown): asserts value is FileMeta {
	const r = asRecord(value, 'FileMeta');
	if (
		typeof r.id !== 'string' ||
		typeof r.name !== 'string' ||
		typeof r.mimeType !== 'string' ||
		typeof r.modifiedTime !== 'string' ||
		typeof r.size !== 'string'
	) {
		throw new Error('Go Wasm returned an unexpected FileMeta shape');
	}
}

function assertConfigureResult(value: unknown): asserts value is ConfigureResult {
	const r = asRecord(value, 'ConfigureResult');
	if (typeof r.configured !== 'boolean' || typeof r.maxPendingRequests !== 'number') {
		throw new Error('Go Wasm returned an unexpected ConfigureResult shape');
	}
}

function assertListFilesResult(value: unknown): asserts value is ListFilesResult {
	const r = asRecord(value, 'ListFilesResult');
	if (
		typeof r.folderId !== 'string' ||
		!Array.isArray(r.files) ||
		typeof r.rotatedRefreshToken !== 'string'
	) {
		throw new Error('Go Wasm returned an unexpected ListFilesResult shape');
	}
	for (const f of r.files) {
		assertFileMeta(f);
	}
}

function assertReadFileResult(value: unknown): asserts value is ReadFileResult {
	const r = asRecord(value, 'ReadFileResult');
	assertFileMeta(r.file);
	if (typeof r.content !== 'string' || typeof r.rotatedRefreshToken !== 'string') {
		throw new Error('Go Wasm returned an unexpected ReadFileResult shape');
	}
}

function assertWriteFileResult(value: unknown): asserts value is WriteFileResult {
	const r = asRecord(value, 'WriteFileResult');
	assertFileMeta(r.file);
	if (typeof r.rotatedRefreshToken !== 'string') {
		throw new Error('Go Wasm returned an unexpected WriteFileResult shape');
	}
}

function assertMergeAndPushResult(value: unknown): asserts value is MergeAndPushResult {
	const r = asRecord(value, 'MergeAndPushResult');
	if (
		typeof r.outcome !== 'string' ||
		typeof r.content !== 'string' ||
		typeof r.remoteContent !== 'string' ||
		typeof r.remoteExisted !== 'boolean' ||
		typeof r.driveUpdated !== 'boolean' ||
		typeof r.localUpdated !== 'boolean' ||
		typeof r.rotatedRefreshToken !== 'string'
	) {
		throw new Error('Go Wasm returned an unexpected MergeAndPushResult shape');
	}
	assertFileMeta(r.file);
}
