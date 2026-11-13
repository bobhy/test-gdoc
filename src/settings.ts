/**
 * Settings tab and re-exported Go-backed settings types for the plugin.
 */

import { App, Notice, PluginSettingTab, type SettingDefinitionItem } from 'obsidian';
import type TestGdocPlugin from './main';
import {
	DEFAULT_SETTINGS,
	type PluginSettings,
} from './settings.generated';

export { DEFAULT_SETTINGS, type PluginSettings };

/**
 * Obsidian settings tab exposing the shared Go `PluginSettings` struct
 * and Google Drive OAuth login/logout controls.
 */
export class GoWasmSettingTab extends PluginSettingTab {
	private readonly plugin: TestGdocPlugin;

	/** Creates the settings tab bound to the active plugin instance. */
	constructor(app: App, plugin: TestGdocPlugin) {
		super(app, plugin);
		this.plugin = plugin;
	}

	/**
	 * Returns declarative setting definitions for Obsidian 1.13.0+ (rendered automatically
	 * and indexed in global settings search).
	 */
	getSettingDefinitions(): SettingDefinitionItem<keyof PluginSettings>[] {
		const authDesc = (authenticated: boolean): string =>
			authenticated
				? 'Connected to Google Drive via vault-share relay.'
				: 'Sign in with Google to authorize Drive file access.';

		return [
			{
				name: 'Google Drive account',
				desc: authDesc(this.plugin.oauth.isAuthenticated),
				render: (setting) => {
					const isAuthenticated = this.plugin.oauth.isAuthenticated;
					setting.setDesc(authDesc(isAuthenticated));
					setting.addButton((button) => {
						button
							.setButtonText(isAuthenticated ? 'Log out' : 'Log in')
							.setCta()
							.onClick(async () => {
								if (this.plugin.oauth.isAuthenticated) {
									await this.plugin.logoutFromDrive();
									this.update();
								} else {
									this.plugin.startDriveLogin();
									new Notice('Complete Google sign-in in your browser.');
								}
							});
					});
				},
			},
			{
				name: 'Drive folder path',
				desc: 'Remote Google Drive folder path for flat Markdown and text files.',
				control: {
					type: 'text',
					key: 'driveFolderPath',
					placeholder: DEFAULT_SETTINGS.driveFolderPath,
				},
			},
			{
				name: 'Local sync folder',
				desc: 'Vault folder storing synced flat Markdown and text files.',
				control: {
					type: 'text',
					key: 'localSyncFolderPath',
					placeholder: DEFAULT_SETTINGS.localSyncFolderPath,
				},
			},
			{
				name: 'Base snapshot folder',
				desc: 'Vault folder holding user-managed base snapshots for 3-way merge.',
				control: {
					type: 'text',
					key: 'baseSnapshotFolderPath',
					placeholder: DEFAULT_SETTINGS.baseSnapshotFolderPath,
				},
			},
			{
				name: 'Max concurrent requests',
				desc: 'Maximum simultaneous Drive requests enforced in Go (1 to 10).',
				control: {
					type: 'slider',
					key: 'maxPendingRequests',
					min: 1,
					max: 10,
					step: 1,
				},
			},
		];
	}
}
