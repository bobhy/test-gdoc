/**
 * Runtime WebAssembly JSPI capability and platform detection for Obsidian startup.
 */

import { Platform } from 'obsidian';
import type { WebAssemblyWithJspi } from './types';

/**
 * Returns an explanatory user-facing error message when the host runtime lacks
 * WebAssembly JSPI (`WebAssembly.Suspending` / `WebAssembly.promising`), or `null` when supported.
 */
export function getJspiUnsupportedReason(
	wasmGlobal: WebAssemblyWithJspi = WebAssembly,
	isIos: boolean = Platform.isIosApp,
): string | null {
	if (isIos) {
		return 'Test GDoc requires WebAssembly JSPI, which is not supported on iOS/iPadOS WebKit.';
	}
	const hasSuspending = typeof wasmGlobal.Suspending === 'function';
	const hasPromising = typeof wasmGlobal.promising === 'function';
	if (!hasSuspending || !hasPromising) {
		return 'Test GDoc requires WebAssembly JSPI (Obsidian desktop installer v1.10.0+ or Android WebView 137+). Please update your Obsidian installer or WebView.';
	}
	return null;
}

/** Throws an Error when WebAssembly JSPI is unavailable on the current platform. */
export function assertJspiSupported(
	wasmGlobal: WebAssemblyWithJspi = WebAssembly,
	isIos: boolean = Platform.isIosApp,
): void {
	const reason = getJspiUnsupportedReason(wasmGlobal, isIos);
	if (reason) {
		throw new Error(reason);
	}
}
