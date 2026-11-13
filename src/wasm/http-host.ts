/**
 * Pass-through HTTP adapter bridging Go Wasm HTTP requests to Obsidian's `requestUrl()`.
 * Throttling, retries, and OAuth token management are handled on the Go side.
 */

import { requestUrl, type RequestUrlParam, type RequestUrlResponse } from 'obsidian';
import type { HttpRequestPayload, HttpResponsePayload } from './types';

/** Function signature matching Obsidian's `requestUrl` for dependency injection in tests. */
export type RequestUrlFn = (request: RequestUrlParam) => Promise<RequestUrlResponse>;

/**
 * Slices a `Uint8Array` view into an exact standalone `ArrayBuffer` for `requestUrl()`.
 */
export function bodyToArrayBuffer(body: Uint8Array): ArrayBuffer {
	if (body.buffer instanceof ArrayBuffer) {
		return body.buffer.slice(body.byteOffset, body.byteOffset + body.byteLength);
	}
	const copy = new Uint8Array(body.byteLength);
	copy.set(body);
	return copy.buffer;
}

/**
 * Executes a single buffered HTTP request via Obsidian's `requestUrl({ ..., throw: false })`.
 */
export async function executeHttpFetch(
	payload: HttpRequestPayload,
	requester: RequestUrlFn = requestUrl,
): Promise<HttpResponsePayload> {
	const param: RequestUrlParam = {
		url: payload.url,
		method: payload.method,
		headers: payload.headers,
		throw: false,
	};
	if (payload.body && payload.body.byteLength > 0) {
		param.body = bodyToArrayBuffer(payload.body);
	}

	const response = await requester(param);
	return {
		status: response.status,
		headers: response.headers,
		body: new Uint8Array(response.arrayBuffer),
	};
}
