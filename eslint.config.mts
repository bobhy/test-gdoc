import obsidianmd from 'eslint-plugin-obsidianmd';
import globals from 'globals';
import { globalIgnores, defineConfig } from 'eslint/config';

export default defineConfig(
	globalIgnores([
		'node_modules',
		'build',
		'dist',
		'esbuild.config.mjs',
		'eslint.config.mts',
		'version-bump.mjs',
		'vitest.config.ts',
		'versions.json',
		'main.js',
		'package.json',
		'package-lock.json',
		'tsconfig.json',
	]),
	{
		languageOptions: {
			globals: {
				...globals.browser,
			},
			parserOptions: {
				projectService: {
					allowDefaultProject: ['manifest.json'],
				},
				tsconfigRootDir: import.meta.dirname,
				extraFileExtensions: ['.json'],
			},
		},
	},
	...obsidianmd.configs.recommended,
	{
		// Sample plugin: "Sample Go Plugin" and "for Obsidian" are intentional
		// here; keep all other manifest checks active.
		files: ['manifest.json'],
		rules: {
			'obsidianmd/validate-manifest': [
				'warn',
				{
					allowedWords: {
						name: ['plugin'],
						description: ['obsidian'],
					},
				},
			],
		},
	},
);
