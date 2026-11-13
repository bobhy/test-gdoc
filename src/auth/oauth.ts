/**
 * OAuth 2.0 login flow, `SecretStorage` persistence, and `vault-share` mutual-exclusion guard.
 *
 * `test-gdoc` reuses `vault-share`'s registered Google OAuth client ID and Cloudflare relay
 * so no separate cloud project or worker deployment is required. Only the browser login
 * redirect happens in TypeScript; token refreshes run in Go against the relay.
 */

import { type App, normalizePath, requestUrl } from 'obsidian';
import type { RequestUrlFn } from '../wasm/http-host';

/** Plugin ID of the sister plugin that shares the same OAuth protocol handler. */
export const VAULT_SHARE_PLUGIN_ID = 'vault-share';

/** Protocol action registered with `registerObsidianProtocolHandler` for OAuth callbacks. */
export const OAUTH_PROTOCOL_ACTION = 'vault-share-auth';

/** Google OAuth 2.0 client ID shared with vault-share. */
export const GOOGLE_CLIENT_ID = '535881224719-5qvscbujq78mpvne1rp3im0cg5cdcu4c.apps.googleusercontent.com';

/** Cloudflare Worker URL relaying Google's authorization callback and token refresh. */
export const RELAY_BASE_URL = 'https://vault-share-auth.bob-hyman.workers.dev';

const GOOGLE_AUTH_URL = 'https://accounts.google.com/o/oauth2/v2/auth';
const GOOGLE_REVOKE_URL = 'https://oauth2.googleapis.com/revoke';
const DRIVE_FILE_SCOPE = 'https://www.googleapis.com/auth/drive.file';

interface ObsidianPluginsRegistry {
	plugins?: {
		manifests?: Record<string, unknown>;
		enabledPlugins?: Set<string>;
	};
}

/**
 * Returns `true` if `vault-share` is installed or enabled in the current Obsidian vault.
 * Used at `onload()` to abort startup cleanly before registering overlapping protocol handlers.
 */
export async function isVaultSharePresent(app: App): Promise<boolean> {
	const registry = (app as unknown as ObsidianPluginsRegistry).plugins;
	if (registry?.enabledPlugins?.has(VAULT_SHARE_PLUGIN_ID)) {
		return true;
	}
	if (registry?.manifests && Object.prototype.hasOwnProperty.call(registry.manifests, VAULT_SHARE_PLUGIN_ID)) {
		return true;
	}
	const manifestPath = normalizePath(`${app.vault.configDir}/plugins/${VAULT_SHARE_PLUGIN_ID}/manifest.json`);
	return await app.vault.adapter.exists(manifestPath);
}

/**
 * Computes the vault-scoped `SecretStorage` key used by `test-gdoc` so its refresh
 * token stays isolated from `vault-share`.
 */
export function refreshTokenSecretKey(vaultName: string): string {
	const safe = vaultName.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '') || 'default';
	return `test-gdoc-${safe}-refresh-token`;
}

/**
 * Manages interactive Google OAuth login and `SecretStorage` refresh-token persistence.
 */
export class OAuthManager {
	private refreshToken = '';
	private pendingState: string | null = null;
	private readonly app: App;
	private readonly requester: RequestUrlFn;

	/** Creates an OAuth manager bound to the given Obsidian `App`. */
	constructor(app: App, requester: RequestUrlFn = requestUrl) {
		this.app = app;
		this.requester = requester;
	}

	/** Returns `true` when a refresh token is currently loaded. */
	get isAuthenticated(): boolean {
		return this.refreshToken.length > 0;
	}

	/** Returns the current refresh token (empty string if logged out). */
	getRefreshToken(): string {
		return this.refreshToken;
	}

	/** Loads any persisted refresh token from Obsidian's `SecretStorage`. */
	loadFromSecretStorage(): string {
		const key = refreshTokenSecretKey(this.app.vault.getName());
		this.refreshToken = this.app.secretStorage.getSecret(key) ?? '';
		return this.refreshToken;
	}

	/** Persists a new or rotated refresh token to `SecretStorage`. */
	saveRefreshToken(token: string): void {
		const trimmed = token.trim();
		this.refreshToken = trimmed;
		const key = refreshTokenSecretKey(this.app.vault.getName());
		this.app.secretStorage.setSecret(key, trimmed);
	}

	/** Clears the stored refresh token from memory and `SecretStorage`. */
	clearRefreshToken(): void {
		this.refreshToken = '';
		const key = refreshTokenSecretKey(this.app.vault.getName());
		this.app.secretStorage.setSecret(key, '');
	}

	/** Generates a Google OAuth 2.0 authorization URL and stores a one-time CSRF state token. */
	getAuthorizationUrl(): string {
		const nonce = generateNonce(24);
		this.pendingState = btoa(JSON.stringify({ app: 'vault-share', nonce }));

		const params = new URLSearchParams({
			client_id: GOOGLE_CLIENT_ID,
			redirect_uri: `${RELAY_BASE_URL}/google/callback`,
			response_type: 'code',
			scope: DRIVE_FILE_SCOPE,
			access_type: 'offline',
			prompt: 'consent',
			state: this.pendingState,
		});
		return `${GOOGLE_AUTH_URL}?${params.toString()}`;
	}

	/** Verifies the `obsidian://vault-share-auth` callback and stores the returned refresh token. */
	handleAuthCallback(params: Record<string, string | undefined>): string {
		if (!this.pendingState) {
			throw new Error('No pending OAuth login flow. Start from plugin settings.');
		}
		if (!params.state || params.state !== this.pendingState) {
			throw new Error('OAuth state mismatch. Please try logging in again.');
		}
		this.pendingState = null;

		const token = params.refresh_token?.trim() ?? '';
		if (!token) {
			throw new Error('Refresh token missing from OAuth callback.');
		}

		this.saveRefreshToken(token);
		return token;
	}

	/** Revokes the stored refresh token at Google's endpoint (best-effort) and clears local storage. */
	async logout(): Promise<void> {
		const token = this.refreshToken;
		if (token) {
			try {
				await this.requester({
					url: `${GOOGLE_REVOKE_URL}?token=${encodeURIComponent(token)}`,
					method: 'POST',
					headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
					throw: false,
				});
			} catch {
				// Best-effort revocation; clear local secret regardless.
			}
		}
		this.clearRefreshToken();
	}
}

const NONCE_CHARS = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789';

function generateNonce(length: number): string {
	const bytes = new Uint8Array(length);
	crypto.getRandomValues(bytes);
	let out = '';
	for (const b of bytes) {
		out += NONCE_CHARS.charAt(b % NONCE_CHARS.length);
	}
	return out;
}
