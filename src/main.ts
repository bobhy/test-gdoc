/**
 * Obsidian plugin entrypoint managing lifecycle, mutual exclusion guard, settings,
 * OAuth callback registration, Drive `ItemView`, and Go Wasm bridge orchestration.
 */

import { Notice, Plugin, normalizePath } from 'obsidian';
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
import { DRIVE_VIEW_TYPE, DriveItemView } from './ui/drive-view';
import {
	ObsidianVaultBackend,
	VaultSyncAdapter,
	isEligibleFileName,
	joinVaultPath,
} from './vault/sync-adapter';
import { GoWasmBridge } from './wasm/bridge';

/**
 * Obsidian plugin validating Go/Wasm Google Drive operations and N-way 3-way merge.
 */
export default class TestGdocPlugin extends Plugin {
	/** Plugin settings synchronized with Go's `settings.PluginSettings` struct. */
	settings!: PluginSettings;
	/** OAuth login and `SecretStorage` manager. */
	oauth!: OAuthManager;
	/** Flat-folder vault and non-destructive base-snapshot adapter. */
	syncAdapter!: VaultSyncAdapter;
	/** Lazy-initialized Go WebAssembly RPC bridge. */
	wasmBridge!: GoWasmBridge;

	private bridgeConfigured = false;
	private settingTab?: GoWasmSettingTab;

	/** Initializes settings, checks `vault-share` exclusion, and defers Wasm loading until first use. */
	async onload(): Promise<void> {
		if (await isVaultSharePresent(this.app)) {
			new Notice(
				'Test GDoc startup aborted: vault-share is installed or active in this vault.',
				0,
			);
			console.error('[test-gdoc] Startup aborted: vault-share is installed or enabled.');
			return;
		}

		await this.loadSettings();

		this.oauth = new OAuthManager(this.app);
		this.oauth.loadFromSecretStorage();

		this.syncAdapter = new VaultSyncAdapter(new ObsidianVaultBackend(this.app.vault));
		this.wasmBridge = new GoWasmBridge((rotatedToken) => {
			this.oauth.saveRefreshToken(rotatedToken);
		});

		this.app.workspace.onLayoutReady(() => {
			void this.seedLocalBaseSnapshots();
		});

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
			id: 'pull-drive-folder',
			name: 'Pull Google Drive folder',
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
		}
	}

	/** Opens or reveals the direct Drive browser `ItemView`. */
	async openDriveView(): Promise<void> {
		const existing = this.app.workspace.getLeavesOfType(DRIVE_VIEW_TYPE)[0];
		if (existing) {
			await this.app.workspace.revealLeaf(existing);
			return;
		}
		const leaf = this.app.workspace.getRightLeaf(false) ?? this.app.workspace.getLeaf(true);
		await leaf.setViewState({ type: DRIVE_VIEW_TYPE, active: true });
		await this.app.workspace.revealLeaf(leaf);
	}

	/** Pulls all flat `.md`/`.txt` files from Drive into `localSyncFolderPath` and seeds missing bases. */
	async pullDriveFolder(): Promise<void> {
		try {
			await this.ensureBridgeConfigured();
			const listRes = await this.wasmBridge.listFiles({
				folderPath: this.settings.driveFolderPath,
			});
			let pulled = 0;
			let seeded = 0;
			for (const file of listRes.files) {
				const readRes = await this.wasmBridge.readFile({ fileId: file.id });
				if (
					await this.syncAdapter.seedMissingBaseFromDrive(
						this.settings.localSyncFolderPath,
						this.settings.baseSnapshotFolderPath,
						file.name,
						readRes.content,
					)
				) {
					seeded++;
				}
				await this.syncAdapter.writeLocalFile(
					this.settings.localSyncFolderPath,
					file.name,
					readRes.content,
				);
				pulled++;
			}
			new Notice(`Pulled ${pulled} file(s) from Drive (seeded ${seeded} missing base file(s)).`);
		} catch (err: unknown) {
			const msg = err instanceof Error ? err.message : String(err);
			console.error('[test-gdoc] Pull failed:', err);
			new Notice(`Drive pull failed: ${msg}`);
		}
	}

	/** Reconciles the active flat note in `localSyncFolderPath` with Drive via Go's 3-way merge. */
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
			const localContent = await this.app.vault.read(activeFile);
			const baseContent = await this.syncAdapter.readBaseSnapshot(
				this.settings.baseSnapshotFolderPath,
				activeFile.name,
			);

			const res = await this.wasmBridge.mergeAndPushFile({
				folderPath: this.settings.driveFolderPath,
				fileName: activeFile.name,
				baseContent: baseContent ?? '',
				localContent,
				hasBase: baseContent !== null,
			});

			if (baseContent === null) {
				await this.syncAdapter.seedMissingBaseFromDrive(
					this.settings.localSyncFolderPath,
					this.settings.baseSnapshotFolderPath,
					activeFile.name,
					res.remoteExisted ? res.remoteContent : localContent,
				);
			}

			if (res.outcome === 'keepBoth') {
				const conflictCopy = await this.syncAdapter.writeConflictCopy(
					this.settings.localSyncFolderPath,
					activeFile.name,
					res.remoteContent,
				);
				new Notice(`Merge fell back to keep-both. Saved remote copy as ${conflictCopy}.`, 0);
				return;
			}

			if (res.localUpdated) {
				await this.app.vault.modify(activeFile, res.content);
			}

			if (res.outcome === 'noop') {
				new Notice(`${activeFile.name} is already in sync with Drive.`);
			} else if (res.outcome === 'folded') {
				new Notice(`Conflict detected in ${activeFile.name}; N-way conflict markers written.`);
			} else {
				new Notice(`Synced ${activeFile.name} with Drive cleanly.`);
			}
		} catch (err: unknown) {
			const msg = err instanceof Error ? err.message : String(err);
			console.error('[test-gdoc] Push failed:', err);
			new Notice(`Drive push failed: ${msg}`);
		}
	}

	private async seedLocalBaseSnapshots(): Promise<void> {
		try {
			const seeded = await this.syncAdapter.seedBaseFromLocal(
				this.settings.localSyncFolderPath,
				this.settings.baseSnapshotFolderPath,
			);
			if (seeded > 0) {
				console.debug(`[test-gdoc] Seeded ${seeded} missing base snapshot(s) from local sync folder.`);
			}
		} catch (err: unknown) {
			console.error('[test-gdoc] Startup base-snapshot seeding failed:', err);
		}
	}
}
