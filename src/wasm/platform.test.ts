import { describe, expect, it } from 'vitest';
import { assertJspiSupported, getJspiUnsupportedReason } from './platform';
import type { WebAssemblyWithJspi } from './types';

function makeWasmGlobal(suspending: boolean, promising: boolean): WebAssemblyWithJspi {
	return {
		Suspending: suspending ? (function MockSuspending() {} as unknown as WebAssemblyWithJspi['Suspending']) : undefined,
		promising: promising ? ((fn) => fn as never) : undefined,
		instantiate: WebAssembly.instantiate,
	};
}

describe('JSPI platform guard', () => {
	it.each([
		{
			name: 'supported desktop/android runtime',
			wasm: makeWasmGlobal(true, true),
			isIos: false,
			wantSupported: true,
		},
		{
			name: 'iOS WebKit unsupported even if shims absent',
			wasm: makeWasmGlobal(false, false),
			isIos: true,
			wantSupported: false,
		},
		{
			name: 'missing WebAssembly.Suspending',
			wasm: makeWasmGlobal(false, true),
			isIos: false,
			wantSupported: false,
		},
		{
			name: 'missing WebAssembly.promising',
			wasm: makeWasmGlobal(true, false),
			isIos: false,
			wantSupported: false,
		},
	])('$name', ({ wasm, isIos, wantSupported }) => {
		const reason = getJspiUnsupportedReason(wasm, isIos);
		if (wantSupported) {
			expect(reason).toBeNull();
			expect(() => assertJspiSupported(wasm, isIos)).not.toThrow();
		} else {
			expect(reason).toBeTruthy();
			expect(() => assertJspiSupported(wasm, isIos)).toThrow();
		}
	});
});
