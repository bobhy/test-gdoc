import { describe, expect, it } from 'vitest';
import {
	VaultSyncAdapter,
	isEligibleFileName,
	type VaultBackend,
	type VaultFileRef,
} from './sync-adapter';

class MemoryVaultBackend implements VaultBackend {
	readonly files = new Map<string, string>();
	readonly folders = new Set<string>();

	listFlatFiles(folderPath: string): VaultFileRef[] {
		const prefix = folderPath ? `${folderPath}/` : '';
		const refs: VaultFileRef[] = [];
		for (const filePath of this.files.keys()) {
			if (!filePath.startsWith(prefix)) {
				continue;
			}
			const rel = filePath.slice(prefix.length);
			if (!rel.includes('/')) {
				refs.push({ name: rel, path: filePath });
			}
		}
		return refs;
	}

	readText(filePath: string): Promise<string | null> {
		return Promise.resolve(this.files.get(filePath) ?? null);
	}

	writeText(filePath: string, content: string): Promise<void> {
		this.files.set(filePath, content);
		return Promise.resolve();
	}

	exists(filePath: string): Promise<boolean> {
		return Promise.resolve(this.files.has(filePath));
	}

	ensureFolder(folderPath: string): Promise<void> {
		this.folders.add(folderPath);
		return Promise.resolve();
	}
}

describe('isEligibleFileName', () => {
	it.each([
		{ name: 'note.md', want: true },
		{ name: 'readme.TXT', want: true },
		{ name: 'sub/note.md', want: false },
		{ name: 'image.png', want: false },
		{ name: '', want: false },
	])('$name -> $want', ({ name, want }) => {
		expect(isEligibleFileName(name)).toBe(want);
	});
});

describe('VaultSyncAdapter', () => {
	it('seeds missing base files from local without overwriting user-edited base files', async () => {
		const backend = new MemoryVaultBackend();
		backend.files.set('sync/a.md', 'local a');
		backend.files.set('sync/b.txt', 'local b');
		backend.files.set('sync/nested/c.md', 'ignored subfolder');
		backend.files.set('sync/pic.png', 'ignored binary');
		backend.files.set('base/b.txt', 'user customized base b');

		const adapter = new VaultSyncAdapter(backend);
		const seeded = await adapter.seedBaseFromLocal('sync', 'base');

		expect(seeded).toBe(1);
		expect(backend.files.get('base/a.md')).toBe('local a');
		expect(backend.files.get('base/b.txt')).toBe('user customized base b');
		expect(backend.files.has('base/nested/c.md')).toBe(false);
		expect(backend.files.has('base/pic.png')).toBe(false);
	});

	it('seeds missing base from Drive only when both base and local are absent', async () => {
		const backend = new MemoryVaultBackend();
		backend.files.set('sync/only-local.md', 'local copy');
		backend.files.set('base/existing.md', 'manual base');

		const adapter = new VaultSyncAdapter(backend);

		expect(await adapter.seedMissingBaseFromDrive('sync', 'base', 'only-local.md', 'drive copy')).toBe(true);
		expect(backend.files.get('base/only-local.md')).toBe('local copy');

		expect(await adapter.seedMissingBaseFromDrive('sync', 'base', 'existing.md', 'drive copy')).toBe(false);
		expect(backend.files.get('base/existing.md')).toBe('manual base');

		expect(await adapter.seedMissingBaseFromDrive('sync', 'base', 'new-from-drive.md', 'drive copy')).toBe(true);
		expect(backend.files.get('base/new-from-drive.md')).toBe('drive copy');
	});

	it('writes uniquely named remote conflict copies for keepBoth', async () => {
		const backend = new MemoryVaultBackend();
		backend.files.set('sync/note.md', 'local version');
		const adapter = new VaultSyncAdapter(backend);

		const first = await adapter.writeConflictCopy('sync', 'note.md', 'remote v1');
		const second = await adapter.writeConflictCopy('sync', 'note.md', 'remote v2');

		expect(first).toBe('note (remote conflict).md');
		expect(second).toBe('note (remote conflict 2).md');
		expect(backend.files.get('sync/note.md')).toBe('local version');
		expect(backend.files.get('sync/note (remote conflict).md')).toBe('remote v1');
		expect(backend.files.get('sync/note (remote conflict 2).md')).toBe('remote v2');
	});
});
