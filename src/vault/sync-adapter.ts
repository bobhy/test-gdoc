/**
 * Flat-folder vault adapter managing local notes and non-destructive base-snapshot seeding.
 */

import { type Vault, normalizePath } from 'obsidian';

/** Reference to a flat file inside a vault folder. */
export interface VaultFileRef {
	name: string;
	path: string;
}

/** Minimal vault abstraction allowing unit tests without full Obsidian mocks. */
export interface VaultBackend {
	listFlatFiles(folderPath: string): VaultFileRef[];
	readText(filePath: string): Promise<string | null>;
	writeText(filePath: string, content: string): Promise<void>;
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

/** Splits `note.md` into `{ stem: 'note', ext: '.md' }`. */
export function splitFileName(fileName: string): { stem: string; ext: string } {
	const dot = fileName.lastIndexOf('.');
	if (dot <= 0) {
		return { stem: fileName, ext: '' };
	}
	return { stem: fileName.slice(0, dot), ext: fileName.slice(dot) };
}

/** Production `VaultBackend` backed by Obsidian's `Vault` API. */
export class ObsidianVaultBackend implements VaultBackend {
	private readonly vault: Vault;

	/** Creates a backend bound to the given Obsidian `Vault`. */
	constructor(vault: Vault) {
		this.vault = vault;
	}

	/** Lists direct non-folder child files in `folderPath`. */
	listFlatFiles(folderPath: string): VaultFileRef[] {
		const folder = this.vault.getFolderByPath(normalizePath(folderPath.trim()));
		if (!folder) {
			return [];
		}
		const refs: VaultFileRef[] = [];
		for (const child of folder.children) {
			if (this.vault.getFileByPath(child.path) !== null) {
				refs.push({ name: child.name, path: child.path });
			}
		}
		return refs;
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

/**
 * Coordinates flat `.md`/`.txt` note I/O in `localSyncFolderPath` and non-destructive
 * base-snapshot reads/seeding in `baseSnapshotFolderPath`.
 */
export class VaultSyncAdapter {
	private readonly backend: VaultBackend;

	/** Creates a sync adapter backed by `backend`. */
	constructor(backend: VaultBackend) {
		this.backend = backend;
	}

	/** Lists eligible flat `.md`/`.txt` files directly inside `localSyncFolderPath`. */
	listFlatLocalFiles(localSyncFolderPath: string): VaultFileRef[] {
		return this.backend
			.listFlatFiles(localSyncFolderPath)
			.filter((ref) => isEligibleFileName(ref.name));
	}

	/** Reads a flat local file inside `localSyncFolderPath`. */
	async readLocalFile(localSyncFolderPath: string, fileName: string): Promise<string | null> {
		if (!isEligibleFileName(fileName)) {
			return null;
		}
		return await this.backend.readText(joinVaultPath(localSyncFolderPath, fileName));
	}

	/** Writes a flat local file inside `localSyncFolderPath`. */
	async writeLocalFile(localSyncFolderPath: string, fileName: string, content: string): Promise<void> {
		await this.backend.writeText(joinVaultPath(localSyncFolderPath, fileName), content);
	}

	/** Reads a user-managed base snapshot from `baseSnapshotFolderPath`, returning `null` if missing. */
	async readBaseSnapshot(baseSnapshotFolderPath: string, fileName: string): Promise<string | null> {
		if (!isEligibleFileName(fileName)) {
			return null;
		}
		return await this.backend.readText(joinVaultPath(baseSnapshotFolderPath, fileName));
	}

	/**
	 * Seeds missing base snapshots from existing local files in `localSyncFolderPath` on startup.
	 * Never overwrites an existing file in `baseSnapshotFolderPath`.
	 */
	async seedBaseFromLocal(
		localSyncFolderPath: string,
		baseSnapshotFolderPath: string,
	): Promise<number> {
		const locals = this.listFlatLocalFiles(localSyncFolderPath);
		let seeded = 0;
		for (const ref of locals) {
			const basePath = joinVaultPath(baseSnapshotFolderPath, ref.name);
			if (await this.backend.exists(basePath)) {
				continue;
			}
			const localContent = await this.backend.readText(ref.path);
			if (localContent !== null) {
				await this.backend.writeText(basePath, localContent);
				seeded++;
			}
		}
		return seeded;
	}

	/**
	 * Ensures any missing base snapshot for a file is populated from the local copy if present,
	 * otherwise from `driveContent`. Never overwrites an existing base snapshot.
	 */
	async seedMissingBaseFromDrive(
		localSyncFolderPath: string,
		baseSnapshotFolderPath: string,
		fileName: string,
		driveContent: string,
	): Promise<boolean> {
		if (!isEligibleFileName(fileName)) {
			return false;
		}
		const basePath = joinVaultPath(baseSnapshotFolderPath, fileName);
		if (await this.backend.exists(basePath)) {
			return false;
		}
		const localContent = await this.readLocalFile(localSyncFolderPath, fileName);
		await this.backend.writeText(basePath, localContent ?? driveContent);
		return true;
	}

	/**
	 * Writes the remote copy to `<stem> (remote conflict).<ext>` (with numeric suffix if needed)
	 * inside `localSyncFolderPath` for `keepBoth` outcomes without overwriting the active note.
	 */
	async writeConflictCopy(
		localSyncFolderPath: string,
		fileName: string,
		remoteContent: string,
	): Promise<string> {
		const { stem, ext } = splitFileName(fileName);
		let candidate = `${stem} (remote conflict)${ext}`;
		let counter = 2;
		while (await this.backend.exists(joinVaultPath(localSyncFolderPath, candidate))) {
			candidate = `${stem} (remote conflict ${counter})${ext}`;
			counter++;
		}
		await this.writeLocalFile(localSyncFolderPath, candidate, remoteContent);
		return candidate;
	}
}
