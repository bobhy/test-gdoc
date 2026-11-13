import { defineConfig } from 'vitest/config';
import path from 'node:path';

export default defineConfig({
	resolve: {
		alias: {
			obsidian: path.resolve(import.meta.dirname, 'src/__mocks__/obsidian.ts'),
		},
	},
	test: {
		include: ['src/**/*.test.ts'],
	},
});
