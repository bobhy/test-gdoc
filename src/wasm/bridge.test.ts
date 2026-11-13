import { afterEach, describe, expect, it, vi } from 'vitest';
import type { RequestUrlParam, RequestUrlResponse } from 'obsidian';
import { bodyToArrayBuffer, executeHttpFetch } from './http-host';

vi.mock('../../build/wasm_exec.js', () => ({}));
vi.mock('../../build/plugin.wasm', () => ({ default: new Uint8Array([0, 97, 115, 109]) }));

import { GoWasmBridge } from './bridge';
import type { WasmBridgeApi, WasmWindowGlobals } from './types';

function makeFakeResponse(status: number, text: string, headers: Record<string, string> = {}): RequestUrlResponse {
	const bytes = new TextEncoder().encode(text);
	return {
		status,
		headers,
		arrayBuffer: bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength),
		json: null,
		text,
	};
}

describe('bodyToArrayBuffer & executeHttpFetch', () => {
	it('slices a Uint8Array view into an exact ArrayBuffer for requestUrl', async () => {
		const backing = new Uint8Array([99, 99, 65, 66, 67, 99]);
		const view = backing.subarray(2, 5); // "ABC"

		let capturedParam: RequestUrlParam | undefined;
		const requester = vi.fn((param: RequestUrlParam) => {
			capturedParam = param;
			return Promise.resolve(makeFakeResponse(201, 'created', { 'x-id': '42' }));
		});

		const res = await executeHttpFetch(
			{
				method: 'POST',
				url: 'https://www.googleapis.com/upload/drive/v3/files',
				headers: { 'Content-Type': 'text/plain' },
				body: view,
			},
			requester,
		);

		expect(capturedParam?.throw).toBe(false);
		expect(capturedParam?.body).toBeInstanceOf(ArrayBuffer);
		expect(new Uint8Array(capturedParam?.body as ArrayBuffer)).toEqual(new Uint8Array([65, 66, 67]));
		expect(bodyToArrayBuffer(view).byteLength).toBe(3);
		expect(res.status).toBe(201);
		expect(res.headers['x-id']).toBe('42');
		expect(new TextDecoder().decode(res.body)).toBe('created');
	});
});

describe('GoWasmBridge', () => {
	afterEach(() => {
		vi.unstubAllGlobals();
		vi.restoreAllMocks();
	});

	it('emits rotatedRefreshToken and tracks lazy Wasm init latency', async () => {
		const fakeGlobals: WasmWindowGlobals & { setTimeout: typeof setTimeout } = {
			setTimeout,
		};

		const shutdownMock = vi.fn();
		const fakeApi: WasmBridgeApi = {
			invoke: vi.fn((method: string) => {
				if (method === 'listFiles') {
					return Promise.resolve(
						JSON.stringify({
							folderId: 'folder-1',
							files: [
								{
									id: 'f1',
									name: 'note.md',
									mimeType: 'text/markdown',
									modifiedTime: '2026-11-12T00:00:00Z',
									size: '10',
								},
							],
							rotatedRefreshToken: 'rotated-secret-99',
						}),
					);
				}
				return Promise.resolve('{}');
			}),
			shutdown: shutdownMock,
		};

		class FakeGoRuntime {
			importObject: WebAssembly.Imports = {};
			run(): Promise<void> {
				fakeGlobals.__testgdoc = fakeApi;
				return Promise.resolve();
			}
		}
		fakeGlobals.Go = FakeGoRuntime;

		vi.stubGlobal('window', fakeGlobals);
		vi.stubGlobal('WebAssembly', {
			instantiate: vi.fn().mockResolvedValue({
				instance: {} as WebAssembly.Instance,
				module: {} as WebAssembly.Module,
			}),
		});

		const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {});
		const rotated: string[] = [];
		const bridge = new GoWasmBridge((tok) => {
			rotated.push(tok);
		});

		const res = await bridge.listFiles({ folderPath: '/test-gdoc' });
		expect(res.folderId).toBe('folder-1');
		expect(res.files).toHaveLength(1);
		expect(rotated).toEqual(['rotated-secret-99']);
		expect(bridge.initDurationMs).not.toBeNull();
		expect(warnSpy).toHaveBeenCalledWith(expect.stringContaining('[test-gdoc] Go Wasm initialized in '));
		expect(typeof fakeGlobals.__testgdocHost?.httpFetch).toBe('function');

		bridge.unload();
		expect(shutdownMock).toHaveBeenCalledOnce();
		expect(fakeGlobals.__testgdocHost).toBeUndefined();
	});
});
