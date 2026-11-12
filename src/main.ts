/**
 * Obsidian plugin entrypoint managing lifecycle, settings persistence, and the Go Wasm bridge.
 */

import { Notice, Plugin } from 'obsidian';
import {
	DEFAULT_SETTINGS,
	GoWasmSettingTab,
	type PluginSettings,
} from './settings';
import { GoWasmBridge } from './wasm/bridge';

/**
 * Sample Obsidian plugin backed by a Go WebAssembly module.
 */
export default class GoWasmPlugin extends Plugin {
	/** Plugin settings synchronized with the Go `settings.PluginSettings` struct. */
	settings!: PluginSettings;
	private readonly wasmBridge = new GoWasmBridge();

	/** Initializes settings, registers UI controls, and defers Wasm loading until first use. */
	async onload(): Promise<void> {
		await this.loadSettings();

		this.addRibbonIcon('cpu', 'Go action', () => {
			void this.runGoAction();
		});

		this.addCommand({
			id: 'run-go-action',
			name: 'Go action',
			callback: () => {
				void this.runGoAction();
			},
		});

		this.addSettingTab(new GoWasmSettingTab(this.app, this));
	}

	/** Shuts down the Go WebAssembly runtime when the plugin is unloaded. */
	onunload(): void {
		this.wasmBridge.unload();
	}

	/** Loads persisted settings merged with defaults generated from Go. */
	async loadSettings(): Promise<void> {
		this.settings = Object.assign(
			{},
			DEFAULT_SETTINGS,
			(await this.loadData()) as Partial<PluginSettings>,
		);
	}

	/** Persists the current plugin settings to Obsidian's data store. */
	async saveSettings(): Promise<void> {
		await this.saveData(this.settings);
	}

	/** Executes the Go Wasm action routine with the current settings and displays the result. */
	async runGoAction(): Promise<void> {
		try {
			const result = await this.wasmBridge.runAction(this.settings);
			new Notice(result.message);
		} catch (err: unknown) {
			const message = err instanceof Error ? err.message : String(err);
			console.error('Go Wasm action failed:', err);
			new Notice(`Go Wasm error: ${message}`);
		}
	}
}
