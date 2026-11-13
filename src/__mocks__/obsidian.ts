/**
 * Minimal Obsidian API mock for Vitest unit tests running in Node.js.
 */

import type { RequestUrlParam, RequestUrlResponse } from 'obsidian';

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
