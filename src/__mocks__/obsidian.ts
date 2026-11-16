/**
 * Minimal Obsidian API mock for Vitest unit tests running in Node.js.
 */

import type { RequestUrlParam, RequestUrlResponse } from 'obsidian';

/** Mirrors Obsidian's `Platform` flags for unit tests. */
export const Platform = {
	isIosApp: false,
};

/** Minimal `Notice` mock for unit tests. */
export class Notice {
	readonly message: string;
	readonly timeout?: number;

	/** Records the notice message and optional duration. */
	constructor(message: string, timeout?: number) {
		this.message = message;
		this.timeout = timeout;
	}
}

/** Mirrors Obsidian's `normalizePath` for flat and nested POSIX vault paths. */
export function normalizePath(path: string): string {
	return path
		.replace(/\\/g, '/')
		.replace(/\/+/g, '/')
		.replace(/^\/+|\/+$/g, '');
}

/** Fallback stub for `requestUrl`; unit tests inject a mock requester directly. */
export function requestUrl(_request: RequestUrlParam): Promise<RequestUrlResponse> {
	return Promise.reject(new Error('requestUrl mock not configured'));
}
