/**
 * Lazy-loading bridge between the Obsidian TypeScript plugin and the embedded Go Wasm module.
 */

import '../../build/wasm_exec.js';
import wasmBytes from '../../build/plugin.wasm';
import type { ActionResult, PluginSettings } from '../settings.generated';
import type { WasmBridgeApi, WasmWindowGlobals } from './types';

const INIT_TIMEOUT_MS = 5000;
const POLL_INTERVAL_MS = 10;

function wasmGlobals(): WasmWindowGlobals {
	return window as unknown as WasmWindowGlobals;
}

/**
 * Manages the lifecycle of the Go WebAssembly runtime and marshals calls between
 * Obsidian TypeScript code and Go.
 */
export class GoWasmBridge {
	private readyPromise: Promise<WasmBridgeApi> | null = null;

	/**
	 * Invokes the Go `RunAction` routine with the provided plugin settings.
	 * Initializes the Wasm runtime lazily on the first call.
	 */
	async runAction(settings: PluginSettings): Promise<ActionResult> {
		const api = await this.ensureInitialized();
		const rawJson = api.runAction(JSON.stringify(settings));
		const parsed: unknown = JSON.parse(rawJson);
		assertActionResult(parsed);
		return parsed;
	}

	/**
	 * Shuts down the Go Wasm runtime and releases registered JS callbacks.
	 */
	unload(): void {
		wasmGlobals().__samplegoplugin?.shutdown();
		this.readyPromise = null;
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
		const GoCtor = wasmGlobals().Go;
		if (!GoCtor) {
			throw new Error('Go wasm_exec.js runtime is not loaded');
		}

		const go = new GoCtor();
		const result = await WebAssembly.instantiate(
			wasmBytes as BufferSource,
			go.importObject,
		);

		void go.run(result.instance).catch((err: unknown) => {
			console.error('Go Wasm runtime exited with error:', err);
		});

		return await waitForBridge(INIT_TIMEOUT_MS);
	}
}

async function waitForBridge(timeoutMs: number): Promise<WasmBridgeApi> {
	const deadline = Date.now() + timeoutMs;
	while (Date.now() < deadline) {
		const api = wasmGlobals().__samplegoplugin;
		if (api) {
			return api;
		}
		await new Promise<void>((resolve) => {
			window.setTimeout(resolve, POLL_INTERVAL_MS);
		});
	}
	throw new Error('Timed out waiting for Go Wasm bridge initialization');
}

function assertActionResult(value: unknown): asserts value is ActionResult {
	if (typeof value !== 'object' || value === null) {
		throw new Error('Go Wasm returned a non-object response');
	}
	const record = value as Record<string, unknown>;
	if (typeof record.error === 'string') {
		throw new Error(`Go Wasm error: ${record.error}`);
	}
	if (
		typeof record.message !== 'string' ||
		typeof record.goVersion !== 'string' ||
		typeof record.platform !== 'string'
	) {
		throw new Error('Go Wasm returned an unexpected ActionResult shape');
	}
}
