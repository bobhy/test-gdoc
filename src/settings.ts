/**
 * Settings tab and re-exported Go-backed settings types for the plugin.
 */

import { App, PluginSettingTab, type SettingDefinitionItem } from 'obsidian';
import type GoWasmPlugin from './main';
import {
	DEFAULT_SETTINGS,
	type PluginSettings,
} from './settings.generated';

export { DEFAULT_SETTINGS, type PluginSettings };

/**
 * Obsidian settings tab exposing the shared Go `PluginSettings` struct
 * and a button to invoke the Go WebAssembly action routine.
 */
export class GoWasmSettingTab extends PluginSettingTab {
	private readonly plugin: GoWasmPlugin;

	/** Creates the settings tab bound to the active plugin instance. */
	constructor(app: App, plugin: GoWasmPlugin) {
		super(app, plugin);
		this.plugin = plugin;
	}

	/**
	 * Returns declarative setting definitions for Obsidian 1.13.0+ (rendered automatically
	 * and indexed in global settings search).
	 */
	getSettingDefinitions(): SettingDefinitionItem<keyof PluginSettings>[] {
		return [
			{
				name: 'Greeting prefix',
				desc: 'Text passed to the compiled action routine.',
				control: {
					type: 'text',
					key: 'greetingPrefix',
					placeholder: DEFAULT_SETTINGS.greetingPrefix,
				},
			},
			{
				name: 'Repeat count',
				desc: 'Number of times the greeting repeats (1 to 10).',
				control: {
					type: 'slider',
					key: 'repeatCount',
					min: 1,
					max: 10,
					step: 1,
				},
			},
			{
				name: 'Uppercase',
				desc: 'Convert the formatted greeting to uppercase.',
				control: {
					type: 'toggle',
					key: 'uppercase',
				},
			},
			{
				name: 'Go action',
				desc: 'Invoke the compiled action routine with the current settings.',
				render: (setting) => {
					setting.addButton((button) =>
						button
							.setButtonText('Run')
							.setCta()
							.onClick(async () => {
								await this.plugin.runGoAction();
							}),
					);
				},
			},
		];
	}
}
