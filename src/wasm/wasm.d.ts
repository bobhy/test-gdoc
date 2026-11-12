/**
 * Ambient module declarations for bundled WebAssembly binaries and Go's wasm_exec.js runtime.
 */

declare module '*.wasm' {
	const wasmBytes: Uint8Array;
	export default wasmBytes;
}

declare module '../../build/wasm_exec.js' {}
