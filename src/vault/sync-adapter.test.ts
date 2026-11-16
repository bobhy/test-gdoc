import { describe, expect, it } from 'vitest';
import { isEligibleFileName, joinVaultPath } from './sync-adapter';

describe('isEligibleFileName', () => {
	it.each([
		{ name: 'note.md', want: true },
		{ name: 'readme.TXT', want: true },
		{ name: 'sub/note.md', want: false },
		{ name: 'sub\\note.md', want: false },
		{ name: 'image.png', want: false },
		{ name: '', want: false },
	])('$name -> $want', ({ name, want }) => {
		expect(isEligibleFileName(name)).toBe(want);
	});
});

describe('joinVaultPath', () => {
	it.each([
		{ folder: '/test-gdoc-sync/', file: 'note.md', want: 'test-gdoc-sync/note.md' },
		{ folder: '', file: 'note.md', want: 'note.md' },
		{ folder: '/', file: 'note.md', want: 'note.md' },
	])('$folder + $file -> $want', ({ folder, file, want }) => {
		expect(joinVaultPath(folder, file)).toBe(want);
	});
});
