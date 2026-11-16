/**
 * Thin Obsidian Vault backend exposing flat-folder stat listing and file I/O primitives
 * consumed by the JSPI Wasm host bridge. Base storage and sync planning live in Go.
 */

import { type FileManager, type Vault, normalizePath } from 'obsidian';
import type { VaultFileStat } from '../settings.generated';

/** Minimal vault abstraction allowing unit tests without full Obsidian mocks. */
export interface VaultBackend {
	listFlatStats(folderPath: string): VaultFileStat[];
	readText(filePath: string): Promise<string | null>;
	writeText(filePath: string, content: string): Promise<void>;
	renameFile(oldPath: string, newPath: string): Promise<void>;
	exists(filePath: string): Promise<boolean>;
	ensureFolder(folderPath: string): Promise<void>;
}

/** Returns `true` when `fileName` is a flat `.md` or `.txt` file name (no directory separators). */
export function isEligibleFileName(fileName: string): boolean {
	const trimmed = fileName.trim();
	if (!trimmed || trimmed.includes('/') || trimmed.includes('\\')) {
		return false;
	}
	const lower = trimmed.toLowerCase();
	return lower.endsWith('.md') || lower.endsWith('.txt');
}

/** Joins a folder path and flat file name using Obsidian vault normalization. */
export function joinVaultPath(folderPath: string, fileName: string): string {
	const cleanFolder = normalizePath(folderPath.trim());
	if (!cleanFolder || cleanFolder === '/') {
		return normalizePath(fileName.trim());
	}
	return normalizePath(`${cleanFolder}/${fileName.trim()}`);
}

/** Production `VaultBackend` backed by Obsidian's `Vault` and optional `FileManager` APIs. */
export class ObsidianVaultBackend implements VaultBackend {
	private readonly vault: Vault;
	private readonly fileManager?: FileManager;

	/** Creates a backend bound to the given Obsidian `Vault` and optional `FileManager`. */
	constructor(vault: Vault, fileManager?: FileManager) {
		this.vault = vault;
		this.fileManager = fileManager;
	}

	/** Lists direct eligible flat `.md`/`.txt` child file stats in `folderPath`. */
	listFlatStats(folderPath: string): VaultFileStat[] {
		const folder = this.vault.getFolderByPath(normalizePath(folderPath.trim()));
		if (!folder) {
			return [];
		}
		const stats: VaultFileStat[] = [];
		for (const child of folder.children) {
			if (!isEligibleFileName(child.name)) {
				continue;
			}
			const file = this.vault.getFileByPath(child.path);
			if (file !== null) {
				stats.push({
					name: file.name,
					path: file.path,
					size: file.stat.size,
					mtimeMs: file.stat.mtime,
				});
			}
		}
		return stats;
	}

	/** Reads UTF-8 text from `filePath`, returning `null` if absent. */
	async readText(filePath: string): Promise<string | null> {
		const file = this.vault.getFileByPath(normalizePath(filePath));
		if (!file) {
			return null;
		}
		return await this.vault.read(file);
	}

	/** Creates or overwrites `filePath` with `content`, creating parent folders if needed. */
	async writeText(filePath: string, content: string): Promise<void> {
		const normalized = normalizePath(filePath);
		const slash = normalized.lastIndexOf('/');
		if (slash > 0) {
			await this.ensureFolder(normalized.slice(0, slash));
		}
		const existing = this.vault.getFileByPath(normalized);
		if (existing) {
			await this.vault.modify(existing, content);
			return;
		}
		await this.vault.create(normalized, content);
	}

	/** Renames an existing Vault file safely via `FileManager.renameFile` when available. */
	async renameFile(oldPath: string, newPath: string): Promise<void> {
		const file = this.vault.getFileByPath(normalizePath(oldPath));
		if (!file) {
			return;
		}
		const normalizedNew = normalizePath(newPath);
		const slash = normalizedNew.lastIndexOf('/');
		if (slash > 0) {
			await this.ensureFolder(normalizedNew.slice(0, slash));
		}
		if (this.fileManager) {
			await this.fileManager.renameFile(file, normalizedNew);
			return;
		}
		await this.vault.rename(file, normalizedNew);
	}

	/** Returns `true` if a file exists at `filePath`. */
	exists(filePath: string): Promise<boolean> {
		return Promise.resolve(this.vault.getFileByPath(normalizePath(filePath)) !== null);
	}

	/** Creates `folderPath` and any missing parent segments recursively. */
	async ensureFolder(folderPath: string): Promise<void> {
		const normalized = normalizePath(folderPath.trim());
		if (!normalized || normalized === '/') {
			return;
		}
		const parts = normalized.split('/');
		let current = '';
		for (const part of parts) {
			current = current ? `${current}/${part}` : part;
			if (!this.vault.getFolderByPath(current)) {
				await this.vault.createFolder(current);
			}
		}
	}
}
