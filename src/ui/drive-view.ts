/**
 * Custom Obsidian ItemView that lists, reads, and edits flat Drive files directly
 * without touching `localSyncFolderPath` or `baseSnapshotFolderPath`.
 *
 * Useful both as a direct Drive browser and as an in-window "second device" simulator
 * to trigger concurrent remote edits for 3-way merge testing.
 */

import { ItemView, Notice, WorkspaceLeaf } from 'obsidian';
import type TestGdocPlugin from '../main';
import type { FileMeta } from '../settings.generated';

/** Registered Obsidian view type identifier for the Drive browser panel. */
export const DRIVE_VIEW_TYPE = 'test-gdoc-drive-view';

/**
 * Side/tab view interacting directly with Google Drive via Go Wasm.
 */
export class DriveItemView extends ItemView {
	private readonly plugin: TestGdocPlugin;
	private files: FileMeta[] = [];
	private selectedFileId = '';

	private statusEl!: HTMLElement;
	private listEl!: HTMLElement;
	private fileNameInput!: HTMLInputElement;
	private editorArea!: HTMLTextAreaElement;

	/** Creates the Drive view attached to the given workspace leaf. */
	constructor(leaf: WorkspaceLeaf, plugin: TestGdocPlugin) {
		super(leaf);
		this.plugin = plugin;
	}

	/** Returns the unique Obsidian view type string. */
	getViewType(): string {
		return DRIVE_VIEW_TYPE;
	}

	/** Returns the human-readable tab title in Obsidian's UI. */
	getDisplayText(): string {
		return 'Google Drive browser';
	}

	/** Returns the Lucide icon name for the view tab. */
	getIcon(): string {
		return 'cloud';
	}

	/** Builds the Drive file list and direct editor DOM when opened. */
	async onOpen(): Promise<void> {
		this.contentEl.empty();
		this.contentEl.addClass('test-gdoc-drive-view');

		const header = this.contentEl.createDiv({ cls: 'test-gdoc-header' });
		const actions = header.createDiv({ cls: 'test-gdoc-actions' });

		const refreshBtn = actions.createEl('button', { text: 'Refresh', cls: 'mod-cta' });
		this.registerDomEvent(refreshBtn, 'click', () => {
			void this.refreshFiles();
		});

		const newBtn = actions.createEl('button', { text: 'New file' });
		this.registerDomEvent(newBtn, 'click', () => {
			this.selectedFileId = '';
			this.fileNameInput.value = 'untitled.md';
			this.fileNameInput.disabled = false;
			this.editorArea.value = '';
			this.setStatus('Creating new Drive file (local vault untouched)');
			this.renderFileList();
		});

		this.statusEl = header.createDiv({
			cls: 'test-gdoc-status',
			text: 'Direct Drive editor (does not touch local sync or base folders)',
		});

		const body = this.contentEl.createDiv({ cls: 'test-gdoc-body' });
		this.listEl = body.createDiv({ cls: 'test-gdoc-file-list' });

		const editorPane = body.createDiv({ cls: 'test-gdoc-editor-pane' });
		const metaBar = editorPane.createDiv({ cls: 'test-gdoc-editor-bar' });
		this.fileNameInput = metaBar.createEl('input', {
			type: 'text',
			cls: 'test-gdoc-filename-input',
			placeholder: 'Select a file or type note.md',
		});

		const saveBtn = metaBar.createEl('button', { text: 'Save to Drive', cls: 'mod-cta' });
		this.registerDomEvent(saveBtn, 'click', () => {
			void this.saveCurrentFile();
		});

		this.editorArea = editorPane.createEl('textarea', {
			cls: 'test-gdoc-textarea',
			placeholder: 'Select a Drive file to inspect or edit remotely...',
		});

		this.renderFileList();
		if (this.plugin.oauth.isAuthenticated) {
			await this.refreshFiles();
		} else {
			this.setStatus('Sign in to Google Drive in settings to list files.');
		}
	}

	/** Clears DOM state when the view closes. */
	onClose(): Promise<void> {
		this.contentEl.empty();
		return Promise.resolve();
	}

	/** Refreshes the flat file list from the configured Drive folder via Go Wasm. */
	async refreshFiles(): Promise<void> {
		try {
			this.setStatus('Loading Drive files...');
			await this.plugin.ensureBridgeConfigured();
			const res = await this.plugin.wasmBridge.listFiles({
				folderPath: this.plugin.settings.driveFolderPath,
			});
			this.files = res.files;
			this.renderFileList();
			this.setStatus(`Loaded ${res.files.length} file(s) from ${this.plugin.settings.driveFolderPath}`);
		} catch (err: unknown) {
			const msg = err instanceof Error ? err.message : String(err);
			this.setStatus(`Error: ${msg}`);
			new Notice(`Drive list failed: ${msg}`);
		}
	}

	private async openDriveFile(file: FileMeta): Promise<void> {
		try {
			this.setStatus(`Reading ${file.name}...`);
			await this.plugin.ensureBridgeConfigured();
			const res = await this.plugin.wasmBridge.readFile({ fileId: file.id });
			this.selectedFileId = res.file.id;
			this.fileNameInput.value = res.file.name;
			this.fileNameInput.disabled = true;
			this.editorArea.value = res.content;
			this.renderFileList();
			this.setStatus(`Editing remote ${res.file.name} (modified ${res.file.modifiedTime})`);
		} catch (err: unknown) {
			const msg = err instanceof Error ? err.message : String(err);
			this.setStatus(`Error: ${msg}`);
			new Notice(`Drive read failed: ${msg}`);
		}
	}

	private async saveCurrentFile(): Promise<void> {
		const fileName = this.fileNameInput.value.trim();
		if (!fileName) {
			new Notice('Enter a file name (e.g. note.md) before saving.');
			return;
		}
		try {
			this.setStatus(`Saving ${fileName} to Drive...`);
			await this.plugin.ensureBridgeConfigured();
			const res = await this.plugin.wasmBridge.writeFile({
				folderPath: this.plugin.settings.driveFolderPath,
				fileId: this.selectedFileId,
				fileName,
				content: this.editorArea.value,
			});
			this.selectedFileId = res.file.id;
			this.fileNameInput.value = res.file.name;
			this.fileNameInput.disabled = true;
			this.setStatus(`Saved ${res.file.name} to Drive (local vault untouched)`);
			new Notice(`Saved ${res.file.name} directly to Drive.`);
			await this.refreshFiles();
		} catch (err: unknown) {
			const msg = err instanceof Error ? err.message : String(err);
			this.setStatus(`Error: ${msg}`);
			new Notice(`Drive save failed: ${msg}`);
		}
	}

	private renderFileList(): void {
		this.listEl.empty();
		if (this.files.length === 0) {
			this.listEl.createDiv({
				cls: 'test-gdoc-empty',
				text: 'No Markdown or text files found.',
			});
			return;
		}

		for (const file of this.files) {
			const itemBtn = this.listEl.createEl('button', {
				cls: 'test-gdoc-file-item',
				text: file.name,
			});
			if (file.id === this.selectedFileId) {
				itemBtn.addClass('is-selected');
			}
			itemBtn.title = file.modifiedTime || file.id;
			this.registerDomEvent(itemBtn, 'click', () => {
				void this.openDriveFile(file);
			});
		}
	}

	private setStatus(text: string): void {
		this.statusEl.setText(text);
	}
}
