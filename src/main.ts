/**
 * Obsidian plugin entrypoint managing lifecycle, platform/mutual-exclusion guards, settings,
 * OAuth callback registration, Vault delete/create/modify tracking, UI views, and Go Wasm bridge.
 */

import { Notice, Plugin, TFile, normalizePath } from 'obsidian';
import {
	OAUTH_PROTOCOL_ACTION,
	OAuthManager,
	isVaultSharePresent,
} from './auth/oauth';
import {
	DEFAULT_SETTINGS,
	GoWasmSettingTab,
	type PluginSettings,
} from './settings';
import type { VaultEventHint } from './settings.generated';
import { DRIVE_VIEW_TYPE, DriveItemView } from './ui/drive-view';
import { SYNC_STATE_VIEW_TYPE, SyncStateView } from './ui/sync-state-view';
import { isEligibleFileName, joinVaultPath } from './vault/sync-adapter';
import { GoWasmBridge } from './wasm/bridge';
import { getJspiUnsupportedReason } from './wasm/platform';

/**
 * Obsidian plugin validating Go/Wasm JSPI + IndexedDB Google Drive sync and 3-way merge.
 */
export default class TestGdocPlugin extends Plugin {
	/** Plugin settings synchronized with Go's `settings.PluginSettings` struct. */
	settings!: PluginSettings;
	/** OAuth login and `SecretStorage` manager. */
	oauth!: OAuthManager;
	/** Lazy-initialized Go WebAssembly JSPI + IndexedDB bridge. */
	wasmBridge!: GoWasmBridge;

	private bridgeConfigured = false;
	private bridgeVaultConfigured = false;
	private settingTab?: GoWasmSettingTab;
	private pendingVaultHints: VaultEventHint[] = [];

	/** Initializes settings, verifies JSPI & vault-share exclusion, and registers Vault hooks. */
	async onload(): Promise<void> {
		if (await isVaultSharePresent(this.app)) {
			new Notice(
				'Test GDoc startup aborted: vault-share is installed or active in this vault.',
				0,
			);
			console.error('[test-gdoc] Startup aborted: vault-share is installed or enabled.');
			return;
		}

		const unsupportedReason = getJspiUnsupportedReason();
		if (unsupportedReason) {
			new Notice(`Test GDoc startup aborted: ${unsupportedReason}`, 0);
			console.error(`[test-gdoc] Startup aborted: ${unsupportedReason}`);
			return;
		}

		await this.loadSettings();

		this.oauth = new OAuthManager(this.app);
		this.oauth.loadFromSecretStorage();

		this.wasmBridge = new GoWasmBridge(this.app, (rotatedToken) => {
			this.oauth.saveRefreshToken(rotatedToken);
		});

		this.registerVaultEventHints();

		this.registerObsidianProtocolHandler(OAUTH_PROTOCOL_ACTION, (params) => {
			try {
				this.oauth.handleAuthCallback(params);
				this.bridgeConfigured = false;
				this.settingTab?.update();
				new Notice('Signed in to Google Drive.');
			} catch (err: unknown) {
				const msg = err instanceof Error ? err.message : String(err);
				new Notice(`Google sign-in failed: ${msg}`);
			}
		});

		this.registerView(DRIVE_VIEW_TYPE, (leaf) => new DriveItemView(leaf, this));
		this.registerView(SYNC_STATE_VIEW_TYPE, (leaf) => new SyncStateView(leaf, this));

		this.addRibbonIcon('cloud', 'Open Google Drive browser', () => {
			void this.openDriveView();
		});

		this.addCommand({
			id: 'open-drive-browser',
			name: 'Open Google Drive browser',
			callback: () => {
				void this.openDriveView();
			},
		});

		this.addCommand({
			id: 'open-sync-state-inspector',
			name: 'Open sync state and IndexedDB inspector',
			callback: () => {
				void this.openSyncStateView();
			},
		});

		this.addCommand({
			id: 'pull-drive-folder',
			name: 'Pull Google Drive folder into local sync folder',
			callback: () => {
				void this.pullDriveFolder();
			},
		});

		this.addCommand({
			id: 'push-active-note',
			name: 'Push active note to Google Drive',
			callback: () => {
				void this.pushActiveNote();
			},
		});

		this.settingTab = new GoWasmSettingTab(this.app, this);
		this.addSettingTab(this.settingTab);
	}

	/** Shuts down the Go WebAssembly runtime when the plugin is unloaded. */
	onunload(): void {
		this.wasmBridge?.unload();
	}

	/** Loads persisted settings merged with defaults generated from Go. */
	async loadSettings(): Promise<void> {
		this.settings = Object.assign(
			{},
			DEFAULT_SETTINGS,
			(await this.loadData()) as Partial<PluginSettings>,
		);
	}

	/** Persists settings and marks Go Wasm config stale so the next call pushes updated settings. */
	async saveSettings(): Promise<void> {
		this.bridgeConfigured = false;
		await this.saveData(this.settings);
	}

	/** Launches Google's interactive OAuth consent page in the system browser. */
	startDriveLogin(): void {
		window.open(this.oauth.getAuthorizationUrl(), '_blank', 'noopener,noreferrer');
	}

	/** Revokes the stored OAuth token and resets Go bridge auth state. */
	async logoutFromDrive(): Promise<void> {
		await this.oauth.logout();
		this.bridgeConfigured = false;
		if (this.wasmBridge.initDurationMs !== null) {
			await this.wasmBridge.configure({
				refreshToken: '',
				maxPendingRequests: this.settings.maxPendingRequests,
			});
		}
		new Notice('Logged out of Google Drive.');
	}

	/** Ensures Wasm and per-vault IndexedDB naming are configured (without requiring Drive auth). */
	async ensureBridgeVaultConfigured(): Promise<void> {
		if (!this.bridgeVaultConfigured) {
			await this.wasmBridge.configure({
				refreshToken: this.oauth.getRefreshToken(),
				maxPendingRequests: this.settings.maxPendingRequests,
			});
			this.bridgeVaultConfigured = true;
			if (this.oauth.isAuthenticated) {
				this.bridgeConfigured = true;
			}
		}
	}

	/** Lazily initializes Wasm and applies the current refresh token and concurrency limit. */
	async ensureBridgeConfigured(): Promise<void> {
		if (!this.oauth.isAuthenticated) {
			throw new Error('Not signed in to Google Drive. Log in from plugin settings first.');
		}
		if (!this.bridgeConfigured) {
			await this.wasmBridge.configure({
				refreshToken: this.oauth.getRefreshToken(),
				maxPendingRequests: this.settings.maxPendingRequests,
			});
			this.bridgeConfigured = true;
			this.bridgeVaultConfigured = true;
		}
	}

	/** Flushes buffered Vault create/delete/modify notifications into Go's IndexedDB store. */
	async flushVaultHints(): Promise<void> {
		if (this.pendingVaultHints.length === 0) {
			return;
		}
		const batch = this.pendingVaultHints.splice(0, this.pendingVaultHints.length);
		await this.wasmBridge.recordVaultHints(batch);
	}

	/** Opens or reveals the direct Drive browser `ItemView`. */
	async openDriveView(): Promise<void> {
		await this.revealOrOpenView(DRIVE_VIEW_TYPE);
	}

	/** Opens or reveals the IndexedDB sync state inspector `ItemView`. */
	async openSyncStateView(): Promise<void> {
		await this.revealOrOpenView(SYNC_STATE_VIEW_TYPE);
	}

	/** Pulls all flat `.md`/`.txt` files from Drive into `localSyncFolderPath` via Go + IndexedDB. */
	async pullDriveFolder(): Promise<void> {
		try {
			await this.ensureBridgeConfigured();
			await this.flushVaultHints();
			const sum = await this.wasmBridge.pullDriveFolder(
				this.settings.driveFolderPath,
				this.settings.localSyncFolderPath,
			);
			const ms = this.wasmBridge.lastOperationDurationMs;
			const timing = ms !== null ? ` in ${ms} ms` : '';
			new Notice(
				`Pulled ${sum.pulled} file(s) from Drive (updated ${sum.updatedIdb} IDB record(s), renamed ${sum.renamed})${timing}.`,
			);
		} catch (err: unknown) {
			const msg = err instanceof Error ? err.message : String(err);
			console.error('[test-gdoc] Pull failed:', err);
			new Notice(`Drive pull failed: ${msg}`);
		}
	}

	/** Reconciles the active flat note in `localSyncFolderPath` with Drive using IndexedDB base state. */
	async pushActiveNote(): Promise<void> {
		const activeFile = this.app.workspace.getActiveFile();
		if (!activeFile) {
			new Notice('Open a Markdown or text note inside the local sync folder first.');
			return;
		}

		const expectedPath = joinVaultPath(this.settings.localSyncFolderPath, activeFile.name);
		if (!isEligibleFileName(activeFile.name) || normalizePath(activeFile.path) !== expectedPath) {
			new Notice(`Active file must be a flat .md/.txt file in ${this.settings.localSyncFolderPath}.`);
			return;
		}

		try {
			await this.ensureBridgeConfigured();
			await this.flushVaultHints();
			const sum = await this.wasmBridge.pushActiveNote(
				this.settings.driveFolderPath,
				this.settings.localSyncFolderPath,
				activeFile.name,
			);
			const ms = this.wasmBridge.lastOperationDurationMs;
			const timing = ms !== null ? ` (${ms} ms)` : '';

			if (sum.outcome === 'keepBoth') {
				new Notice(
					`Merge fell back to keep-both. Saved remote copy as ${sum.conflictCopyName}${timing}.`,
					0,
				);
			} else if (sum.outcome === 'noop') {
				new Notice(`${activeFile.name} is already in sync with Drive${timing}.`);
			} else if (sum.outcome === 'folded') {
				new Notice(
					`Conflict detected in ${activeFile.name}; N-way conflict markers written${timing}.`,
				);
			} else {
				new Notice(`Synced ${activeFile.name} with Drive cleanly${timing}.`);
			}
		} catch (err: unknown) {
			const msg = err instanceof Error ? err.message : String(err);
			console.error('[test-gdoc] Push failed:', err);
			new Notice(`Drive push failed: ${msg}`);
		}
	}

	private async revealOrOpenView(viewType: string): Promise<void> {
		const existing = this.app.workspace.getLeavesOfType(viewType)[0];
		if (existing) {
			await this.app.workspace.revealLeaf(existing);
			return;
		}
		const leaf = this.app.workspace.getRightLeaf(false) ?? this.app.workspace.getLeaf(true);
		await leaf.setViewState({ type: viewType, active: true });
		await this.app.workspace.revealLeaf(leaf);
	}

	private registerVaultEventHints(): void {
		const queueHint = (kind: 'create' | 'delete' | 'modify', abstractFile: unknown) => {
			if (!(abstractFile instanceof TFile)) {
				return;
			}
			const expectedPath = joinVaultPath(this.settings.localSyncFolderPath, abstractFile.name);
			if (
				!isEligibleFileName(abstractFile.name) ||
				normalizePath(abstractFile.path) !== expectedPath
			) {
				return;
			}
			this.pendingVaultHints.push({
				kind,
				fileName: abstractFile.name,
				size: abstractFile.stat.size,
				mtimeMs: abstractFile.stat.mtime,
				timestampMs: Date.now(),
			});
		};

		this.registerEvent(this.app.vault.on('create', (f) => queueHint('create', f)));
		this.registerEvent(this.app.vault.on('delete', (f) => queueHint('delete', f)));
		this.registerEvent(this.app.vault.on('modify', (f) => queueHint('modify', f)));
	}
}
