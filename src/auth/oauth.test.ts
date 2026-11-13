import { describe, expect, it, vi } from 'vitest';
import type { App } from 'obsidian';
import {
	OAuthManager,
	VAULT_SHARE_PLUGIN_ID,
	isVaultSharePresent,
	refreshTokenSecretKey,
} from './oauth';

function makeMockApp(options: {
	vaultName?: string;
	enabledPlugins?: string[];
	manifests?: Record<string, unknown>;
	existingPaths?: string[];
}): App {
	const secrets = new Map<string, string>();
	const existing = new Set(options.existingPaths ?? []);
	return {
		vault: {
			configDir: '.obsidian',
			getName: () => options.vaultName ?? 'My Vault',
			adapter: {
				exists: vi.fn((p: string) => Promise.resolve(existing.has(p))),
			},
		},
		secretStorage: {
			getSecret: (id: string) => secrets.get(id) ?? null,
			setSecret: (id: string, val: string) => {
				secrets.set(id, val);
			},
		},
		plugins: {
			enabledPlugins: new Set(options.enabledPlugins ?? []),
			manifests: options.manifests ?? {},
		},
	} as unknown as App;
}

describe('isVaultSharePresent', () => {
	it('returns false when vault-share is absent', async () => {
		const app = makeMockApp({});
		expect(await isVaultSharePresent(app)).toBe(false);
	});

	it('detects installed-but-disabled vault-share via manifests or manifest.json', async () => {
		const appViaManifest = makeMockApp({ manifests: { [VAULT_SHARE_PLUGIN_ID]: { id: VAULT_SHARE_PLUGIN_ID } } });
		expect(await isVaultSharePresent(appViaManifest)).toBe(true);

		const appViaDisk = makeMockApp({ existingPaths: ['.obsidian/plugins/vault-share/manifest.json'] });
		expect(await isVaultSharePresent(appViaDisk)).toBe(true);
	});

	it('detects active vault-share via enabledPlugins', async () => {
		const app = makeMockApp({ enabledPlugins: [VAULT_SHARE_PLUGIN_ID] });
		expect(await isVaultSharePresent(app)).toBe(true);
	});
});

describe('OAuthManager', () => {
	it('verifies CSRF state and persists refresh token in vault-scoped SecretStorage', () => {
		const app = makeMockApp({ vaultName: 'Test Vault 1' });
		const oauth = new OAuthManager(app);

		const authUrl = oauth.getAuthorizationUrl();
		const state = new URL(authUrl).searchParams.get('state') ?? '';
		expect(state).not.toBe('');

		expect(() => oauth.handleAuthCallback({ state: 'wrong-state', refresh_token: 'rt' })).toThrow('state mismatch');

		// Restore pending state by regenerating and matching exact state.
		const authUrl2 = oauth.getAuthorizationUrl();
		const state2 = new URL(authUrl2).searchParams.get('state') ?? '';
		const saved = oauth.handleAuthCallback({ state: state2, refresh_token: 'refresh-abc' });
		expect(saved).toBe('refresh-abc');

		const key = refreshTokenSecretKey('Test Vault 1');
		expect(key).toBe('test-gdoc-test-vault-1-refresh-token');
		expect(app.secretStorage.getSecret(key)).toBe('refresh-abc');

		const reloaded = new OAuthManager(app);
		expect(reloaded.loadFromSecretStorage()).toBe('refresh-abc');
		expect(reloaded.isAuthenticated).toBe(true);
	});
});
