/**
 * Shared TypeScript interfaces for the Go WebAssembly runtime bridge and host HTTP pass-through.
 */

/** Shape of the global `Go` class installed by Go's `wasm_exec.js`. */
export interface GoRuntimeInstance {
	importObject: WebAssembly.Imports;
	run(instance: WebAssembly.Instance): Promise<void>;
}

/** Constructor for Go's `wasm_exec.js` runtime class. */
export type GoRuntimeConstructor = new () => GoRuntimeInstance;

/** Serialized HTTP request sent from Go Wasm across the host bridge. */
export interface HttpRequestPayload {
	method: string;
	url: string;
	headers: Record<string, string>;
	body?: Uint8Array;
}

/** Serialized HTTP response returned from the TypeScript host to Go Wasm. */
export interface HttpResponsePayload {
	status: number;
	headers: Record<string, string>;
	body: Uint8Array;
}

/** Host callbacks mounted on `window.__testgdocHost` for Go Wasm to invoke. */
export interface WasmHostCallbacks {
	httpFetch(req: HttpRequestPayload): Promise<HttpResponsePayload>;
}

/** Functions exported onto `window.__testgdoc` by `go/cmd/plugin`. */
export interface WasmBridgeApi {
	invoke(method: string, payloadJson: string): Promise<string>;
	shutdown(): void;
}

/** Window extension exposing the Go runtime constructor, host callbacks, and Wasm bridge. */
export interface WasmWindowGlobals {
	Go?: GoRuntimeConstructor;
	__testgdoc?: WasmBridgeApi;
	__testgdocHost?: WasmHostCallbacks;
}
