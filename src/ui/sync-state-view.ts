/**
 * Obsidian ItemView and Modal for inspecting IndexedDB sync state records (Drive metadata,
 * Vault metadata, sync relations, rename matches) and managing stored common base snapshots.
 */

import { type App, ItemView, Modal, Notice, WorkspaceLeaf } from 'obsidian';
import type TestGdocPlugin from '../main';
import type { SyncRecord } from '../settings.generated';

/** Registered Obsidian view type identifier for the Sync State & IDB Inspector panel. */
export const SYNC_STATE_VIEW_TYPE = 'test-gdoc-sync-state-view';

/** Modal allowing manual inspection, editing, or clearing of an IndexedDB common base entry. */
export class BaseEditorModal extends Modal {
	private readonly fileName: string;
	private readonly bridge: TestGdocPlugin['wasmBridge'];
	private readonly onSaved: () => void;

	/** Creates the base editor modal for `fileName`. */
	constructor(
		app: App,
		fileName: string,
		bridge: TestGdocPlugin['wasmBridge'],
		onSaved: () => void,
	) {
		super(app);
		this.fileName = fileName;
		this.bridge = bridge;
		this.onSaved = onSaved;
	}

	/** Loads any stored IndexedDB base content and renders editor controls. */
	onOpen(): void {
		this.titleEl.setText(`Common base: ${this.fileName}`);
		this.contentEl.empty();

		const infoEl = this.contentEl.createDiv({
			cls: 'test-gdoc-status',
			text: 'Loading stored IndexedDB base snapshot...',
		});
		const textarea = this.contentEl.createEl('textarea', {
			cls: 'test-gdoc-textarea',
			placeholder: 'Base snapshot is currently null (absent from bases store).',
		});

		const actions = this.contentEl.createDiv({ cls: 'test-gdoc-actions' });
		const saveBtn = actions.createEl('button', { text: 'Save base', cls: 'mod-cta' });
		const clearBtn = actions.createEl('button', { text: 'Clear base' });

		saveBtn.addEventListener('click', () => {
			void this.saveBase(textarea.value);
		});
		clearBtn.addEventListener('click', () => {
			void this.clearBase();
		});

		void this.loadInitial(infoEl, textarea);
	}

	/** Clears modal DOM on close. */
	onClose(): void {
		this.contentEl.empty();
	}

	private async loadInitial(infoEl: HTMLElement, textarea: HTMLTextAreaElement): Promise<void> {
		try {
			const res = await this.bridge.getBaseSnapshot(this.fileName);
			if (res.hasBase) {
				infoEl.setText(`Stored base SHA-256: ${res.sha256.slice(0, 12)}...`);
				textarea.value = res.content;
			} else {
				infoEl.setText('No stored base (null). Enter text and save to set a manual common base.');
				textarea.value = '';
			}
		} catch (err: unknown) {
			const msg = err instanceof Error ? err.message : String(err);
			infoEl.setText(`Load failed: ${msg}`);
		}
	}

	private async saveBase(content: string): Promise<void> {
		try {
			await this.bridge.setBaseSnapshot(this.fileName, content);
			new Notice(`Saved IndexedDB base for ${this.fileName}.`);
			this.onSaved();
			this.close();
		} catch (err: unknown) {
			const msg = err instanceof Error ? err.message : String(err);
			new Notice(`Failed to save base: ${msg}`);
		}
	}

	private async clearBase(): Promise<void> {
		try {
			await this.bridge.clearBaseSnapshot(this.fileName);
			new Notice(`Cleared IndexedDB base for ${this.fileName}.`);
			this.onSaved();
			this.close();
		} catch (err: unknown) {
			const msg = err instanceof Error ? err.message : String(err);
			new Notice(`Failed to clear base: ${msg}`);
		}
	}
}

/** Side/tab view showing IndexedDB synchronization state records, renames, and base controls. */
export class SyncStateView extends ItemView {
	private readonly plugin: TestGdocPlugin;
	private records: SyncRecord[] = [];
	private statusEl!: HTMLElement;
	private listEl!: HTMLElement;

	/** Creates the Sync State & IndexedDB Inspector view attached to `leaf`. */
	constructor(leaf: WorkspaceLeaf, plugin: TestGdocPlugin) {
		super(leaf);
		this.plugin = plugin;
	}

	/** Returns the unique Obsidian view type string. */
	getViewType(): string {
		return SYNC_STATE_VIEW_TYPE;
	}

	/** Returns the human-readable tab title in Obsidian's UI. */
	getDisplayText(): string {
		return 'Sync state & IndexedDB';
	}

	/** Returns the Lucide icon name for the view tab. */
	getIcon(): string {
		return 'database';
	}

	/** Builds toolbar actions and renders the IndexedDB record table. */
	async onOpen(): Promise<void> {
		this.contentEl.empty();
		this.contentEl.addClass('test-gdoc-drive-view');

		const header = this.contentEl.createDiv({ cls: 'test-gdoc-header' });
		const actions = header.createDiv({ cls: 'test-gdoc-actions' });

		const scanVaultBtn = actions.createEl('button', { text: 'Scan vault', cls: 'mod-cta' });
		this.registerDomEvent(scanVaultBtn, 'click', () => {
			void this.refreshState(false);
		});

		const scanAllBtn = actions.createEl('button', { text: 'Scan vault + Drive' });
		this.registerDomEvent(scanAllBtn, 'click', () => {
			void this.refreshState(true);
		});

		const benchBtn = actions.createEl('button', { text: 'Benchmark IndexedDB' });
		this.registerDomEvent(benchBtn, 'click', () => {
			void this.runBenchmark();
		});

		this.statusEl = header.createDiv({
			cls: 'test-gdoc-status',
			text: 'IndexedDB sync state store (bases stored out of Vault/Drive)',
		});

		this.listEl = this.contentEl.createDiv({ cls: 'test-gdoc-sync-table' });
		await this.refreshState(false);
	}

	/** Clears DOM state when the view closes. */
	onClose(): Promise<void> {
		this.contentEl.empty();
		return Promise.resolve();
	}

	/** Scans Vault (and optionally Drive) through Go and displays updated IndexedDB records. */
	async refreshState(includeDrive: boolean): Promise<void> {
		try {
			this.setStatus(includeDrive ? 'Scanning Vault and Drive...' : 'Scanning Vault...');
			if (includeDrive) {
				await this.plugin.ensureBridgeConfigured();
			} else {
				await this.plugin.ensureBridgeVaultConfigured();
			}
			await this.plugin.flushVaultHints();
			const res = await this.plugin.wasmBridge.getSyncState({
				driveFolderPath: this.plugin.settings.driveFolderPath,
				localSyncFolderPath: this.plugin.settings.localSyncFolderPath,
				includeDrive,
			});
			this.records = res.records;
			this.renderRecords();
			const ms = this.plugin.wasmBridge.lastOperationDurationMs ?? 0;
			this.setStatus(
				`${res.records.length} record(s) | hashed=${res.hashedVaultFiles} renamedVault=${res.renamedVaultFiles} renamedDrive=${res.renamedDriveFiles} (${ms} ms)`,
			);
		} catch (err: unknown) {
			const msg = err instanceof Error ? err.message : String(err);
			this.setStatus(`Scan error: ${msg}`);
			new Notice(`Sync scan failed: ${msg}`);
		}
	}

	private async runBenchmark(): Promise<void> {
		try {
			this.setStatus('Running 100-record IndexedDB benchmark...');
			await this.plugin.ensureBridgeVaultConfigured();
			const res = await this.plugin.wasmBridge.runIdbBenchmark(100);
			const summary = `IDB bench (${res.count}): write=${res.writeMs.toFixed(1)} ms, scan=${res.scanMs.toFixed(1)} ms, getBase=${res.getMs.toFixed(1)} ms, clear=${res.clearMs.toFixed(1)} ms`;
			this.setStatus(summary);
			new Notice(summary, 6000);
		} catch (err: unknown) {
			const msg = err instanceof Error ? err.message : String(err);
			this.setStatus(`Benchmark failed: ${msg}`);
			new Notice(`IndexedDB benchmark failed: ${msg}`);
		}
	}

	private renderRecords(): void {
		this.listEl.empty();
		if (this.records.length === 0) {
			this.listEl.createDiv({
				cls: 'test-gdoc-empty',
				text: 'No sync records in IndexedDB yet. Add notes or run a scan.',
			});
			return;
		}

		for (const rec of this.records) {
			const row = this.listEl.createDiv({ cls: 'test-gdoc-sync-row' });
			const top = row.createDiv({ cls: 'test-gdoc-sync-top' });
			top.createSpan({ cls: 'test-gdoc-sync-name', text: rec.fileName });
			top.createSpan({
				cls: `test-gdoc-relation is-${rec.relation}`,
				text: rec.relation,
			});

			const metaParts: string[] = [];
			if (rec.renamedFrom) {
				metaParts.push(`renamed from ${rec.renamedFrom}`);
			}
			if (rec.vault) {
				const vHash = rec.vault.sha256 ? rec.vault.sha256.slice(0, 8) : 'pending';
				metaParts.push(`vault: ${rec.vault.size} B (${vHash})`);
			} else {
				metaParts.push('vault: absent');
			}
			if (rec.drive) {
				const dHash = (rec.drive.sha256Checksum || rec.drive.md5Checksum || 'none').slice(0, 8);
				metaParts.push(`drive: ${rec.drive.size} B (${dHash})`);
			} else {
				metaParts.push('drive: absent');
			}
			if (rec.hasBase && rec.baseMeta) {
				metaParts.push(`base: ${rec.baseMeta.size} B (${rec.baseMeta.sha256.slice(0, 8)})`);
			} else {
				metaParts.push('base: null');
			}

			row.createDiv({ cls: 'test-gdoc-sync-meta', text: metaParts.join(' | ') });

			const btnRow = row.createDiv({ cls: 'test-gdoc-actions' });
			const baseBtn = btnRow.createEl('button', {
				text: rec.hasBase ? 'Edit base' : 'Set base',
			});
			this.registerDomEvent(baseBtn, 'click', () => {
				new BaseEditorModal(this.app, rec.fileName, this.plugin.wasmBridge, () => {
					void this.refreshState(false);
				}).open();
			});
		}
	}

	private setStatus(text: string): void {
		this.statusEl.setText(text);
	}
}
