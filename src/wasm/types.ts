/**
 * Shared TypeScript interfaces for the Go WebAssembly runtime bridge.
 */

/** Shape of the global `Go` class installed by Go's `wasm_exec.js`. */
export interface GoRuntimeInstance {
	importObject: WebAssembly.Imports;
	run(instance: WebAssembly.Instance): Promise<void>;
}

/** Constructor for Go's `wasm_exec.js` runtime class. */
export type GoRuntimeConstructor = new () => GoRuntimeInstance;

/** Functions exported onto the global object by `go/cmd/plugin`. */
export interface WasmBridgeApi {
	runAction(settingsJson: string): string;
	shutdown(): void;
}

/** Window extension exposing the Go runtime constructor and plugin Wasm bridge. */
export interface WasmWindowGlobals {
	Go?: GoRuntimeConstructor;
	__testgdoc?: WasmBridgeApi;
}
