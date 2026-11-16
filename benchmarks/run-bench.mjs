#!/usr/bin/env node
/**
 * Size & runtime benchmark harness comparing cached `benchmarks/baseline-main` against
 * the current `efficiency-hacks` build (Hybrid JSPI + hack-pad/go-indexeddb) in clean Node 26
 * V8 subprocesses backed by fake-indexeddb.
 *
 * Usage:
 *   node benchmarks/run-bench.mjs
 */

import { execFileSync } from 'node:child_process';
import crypto from 'node:crypto';
import fs from 'node:fs';
import path from 'node:path';
import vm from 'node:vm';
import zlib from 'node:zlib';
import { performance } from 'node:perf_hooks';

const ROOT = path.resolve(import.meta.dirname, '..');
const BASELINE_DIR = path.join(ROOT, 'benchmarks', 'baseline-main');

function sha256Hex(text) {
	return crypto.createHash('sha256').update(text, 'utf8').digest('hex');
}

function md5Hex(text) {
	return crypto.createHash('md5').update(text, 'utf8').digest('hex');
}

function measureArtifacts() {
	const entries = [
		{ name: 'main.js', base: path.join(BASELINE_DIR, 'main.js'), cur: path.join(ROOT, 'main.js') },
		{ name: 'plugin.wasm', base: path.join(BASELINE_DIR, 'plugin.wasm'), cur: path.join(ROOT, 'build', 'plugin.wasm') },
		{ name: 'wasm_exec.js', base: path.join(BASELINE_DIR, 'wasm_exec.js'), cur: path.join(ROOT, 'build', 'wasm_exec.js') },
		{ name: 'manifest.json', base: path.join(BASELINE_DIR, 'manifest.json'), cur: path.join(ROOT, 'manifest.json') },
		{ name: 'styles.css', base: path.join(BASELINE_DIR, 'styles.css'), cur: path.join(ROOT, 'styles.css') },
	];
	return entries.map(({ name, base, cur }) => {
		const bBuf = fs.readFileSync(base);
		const cBuf = fs.readFileSync(cur);
		const bGz = zlib.gzipSync(bBuf, { level: 9 }).length;
		const cGz = zlib.gzipSync(cBuf, { level: 9 }).length;
		return {
			name,
			baseRaw: bBuf.length,
			curRaw: cBuf.length,
			deltaRaw: cBuf.length - bBuf.length,
			baseGz: bGz,
			curGz: cGz,
			deltaGz: cGz - bGz,
		};
	});
}

function makeMockObsidian(simulatedHttpMs) {
	const files = new Map();
	const folders = new Set(['', 'test-gdoc-sync', 'test-gdoc-base']);
	const eventHandlers = new Map();
	let nextMtime = 1000;

	function norm(p) {
		return p.replace(/\\/g, '/').replace(/\/+/g, '/').replace(/^\/+|\/+$/g, '');
	}

	class TFile {
		constructor(p, content) {
			this.path = p;
			const parts = p.split('/');
			this.name = parts[parts.length - 1];
			this.stat = { size: Buffer.byteLength(content, 'utf8'), mtime: nextMtime, ctime: nextMtime };
		}
	}

	function setVaultFile(p, content) {
		nextMtime += 1;
		const rec = { content, mtime: nextMtime, size: Buffer.byteLength(content, 'utf8') };
		files.set(p, rec);
		return toTFile(p, rec);
	}

	function toTFile(p, rec) {
		const f = new TFile(p, rec.content);
		f.stat = { size: rec.size, mtime: rec.mtime, ctime: rec.mtime };
		return f;
	}

	function emitVault(kind, fileObj) {
		for (const fn of eventHandlers.get(kind) ?? []) {
			fn(fileObj);
		}
	}

	const driveStore = new Map();
	for (let i = 0; i < 40; i++) {
		const name = `note-${String(i).padStart(3, '0')}.md`;
		const body = `# Note ${i}\n` + `Line A for ${name} with shared text.\n`.repeat(80);
		driveStore.set(`f-${i}`, {
			id: `f-${i}`,
			name,
			content: body,
			mimeType: 'text/markdown',
			modifiedTime: '2026-11-15T00:00:00Z',
			size: String(Buffer.byteLength(body, 'utf8')),
			sha256Checksum: sha256Hex(body),
			md5Checksum: md5Hex(body),
		});
	}

	const vault = {
		configDir: '.obsidian',
		getName: () => 'bench-vault',
		adapter: { exists: async () => false },
		on: (event, cb) => {
			const list = eventHandlers.get(event) ?? [];
			list.push(cb);
			eventHandlers.set(event, list);
			return { event, cb };
		},
		getFolderByPath: (p) => {
			const clean = norm(p);
			if (!folders.has(clean)) return null;
			const prefix = clean ? `${clean}/` : '';
			const children = [];
			for (const [fp, rec] of files.entries()) {
				if (fp.startsWith(prefix) && !fp.slice(prefix.length).includes('/')) {
					children.push(toTFile(fp, rec));
				}
			}
			return { path: clean, children };
		},
		getFileByPath: (p) => {
			const clean = norm(p);
			const rec = files.get(clean);
			return rec ? toTFile(clean, rec) : null;
		},
		read: async (f) => files.get(f.path)?.content ?? '',
		modify: async (f, data) => {
			const tf = setVaultFile(f.path, data);
			emitVault('modify', tf);
		},
		create: async (p, data) => {
			const clean = norm(p);
			const tf = setVaultFile(clean, data);
			emitVault('create', tf);
			return tf;
		},
		rename: async (f, newPath) => {
			const cleanNew = norm(newPath);
			const rec = files.get(f.path);
			if (rec) {
				files.delete(f.path);
				files.set(cleanNew, rec);
			}
		},
		createFolder: async (p) => {
			folders.add(norm(p));
		},
	};

	const fileManager = {
		renameFile: async (f, newPath) => vault.rename(f, newPath),
	};

	const secrets = new Map([['test-gdoc-bench-vault-refresh-token', 'bench-refresh-token']]);
	let activeFile = null;

	const app = {
		vault,
		fileManager,
		workspace: {
			onLayoutReady: () => {},
			getActiveFile: () => activeFile,
			getLeavesOfType: () => [],
			getRightLeaf: () => null,
			getLeaf: () => null,
			revealLeaf: async () => {},
		},
		secretStorage: {
			getSecret: (k) => secrets.get(k) ?? null,
			setSecret: (k, v) => secrets.set(k, v),
		},
		plugins: { enabledPlugins: new Set(), manifests: {} },
	};

	const decoder = new TextDecoder();
	const encoder = new TextEncoder();

	function serializeDriveMeta(entry) {
		return {
			id: entry.id,
			name: entry.name,
			mimeType: entry.mimeType,
			modifiedTime: entry.modifiedTime,
			size: String(Buffer.byteLength(entry.content, 'utf8')),
			sha256Checksum: sha256Hex(entry.content),
			md5Checksum: md5Hex(entry.content),
		};
	}

	async function requestUrl(req) {
		if (simulatedHttpMs > 0) {
			await new Promise((r) => setTimeout(r, simulatedHttpMs));
		}
		const url = req.url;
		const jsonResp = (status, obj) => {
			const txt = JSON.stringify(obj);
			const u8 = encoder.encode(txt);
			return {
				status,
				headers: { 'content-type': 'application/json' },
				arrayBuffer: u8.buffer.slice(u8.byteOffset, u8.byteOffset + u8.byteLength),
				json: obj,
				text: txt,
			};
		};
		const textResp = (status, txt) => {
			const u8 = encoder.encode(txt);
			return {
				status,
				headers: { 'content-type': 'text/plain' },
				arrayBuffer: u8.buffer.slice(u8.byteOffset, u8.byteOffset + u8.byteLength),
				json: null,
				text: txt,
			};
		};

		if (url.includes('/google/token/refresh')) {
			return jsonResp(200, { access_token: 'bench-access-tok', expires_in: 3600 });
		}
		if (url.includes('alt=media') && req.method === 'GET') {
			const match = url.match(/\/drive\/v3\/files\/([^?]+)/);
			const id = match?.[1] ?? '';
			const entry = driveStore.get(id);
			return entry ? textResp(200, entry.content) : textResp(404, 'not found');
		}
		if (req.method === 'GET' && /\/drive\/v3\/files\/([^?]+)/.test(url)) {
			const id = url.match(/\/drive\/v3\/files\/([^?]+)/)?.[1] ?? '';
			const entry = driveStore.get(id);
			return entry ? jsonResp(200, serializeDriveMeta(entry)) : textResp(404, 'not found');
		}
		if (url.includes('/drive/v3/files') && req.method === 'GET') {
			const decodedUrl = decodeURIComponent(url.replace(/\+/g, ' '));
			if (decodedUrl.includes("mimeType='application/vnd.google-apps.folder'") && !decodedUrl.includes('!=')) {
				return jsonResp(200, { files: [{ id: 'folder-1', name: 'test-gdoc', mimeType: 'application/vnd.google-apps.folder' }] });
			}
			const list = Array.from(driveStore.values()).map(serializeDriveMeta);
			return jsonResp(200, { files: list });
		}
		if (url.includes('/upload/drive/v3/files/') && req.method === 'PATCH') {
			const id = url.match(/\/upload\/drive\/v3\/files\/([^?]+)/)?.[1] ?? '';
			const entry = driveStore.get(id);
			if (entry && req.body instanceof ArrayBuffer) {
				const raw = decoder.decode(new Uint8Array(req.body));
				const start = raw.lastIndexOf('\r\n\r\n');
				const end = raw.lastIndexOf('\r\n--');
				if (start > 0 && end > start) {
					entry.content = raw.slice(start + 4, end);
				}
			}
			return entry ? jsonResp(200, serializeDriveMeta(entry)) : textResp(404, 'not found');
		}
		if (url.includes('/drive/v3/files/') && req.method === 'PATCH') {
			const id = url.match(/\/drive\/v3\/files\/([^?]+)/)?.[1] ?? '';
			const entry = driveStore.get(id);
			if (entry && req.body instanceof ArrayBuffer) {
				const meta = JSON.parse(decoder.decode(new Uint8Array(req.body)));
				if (meta.name) entry.name = meta.name;
			}
			return entry ? jsonResp(200, serializeDriveMeta(entry)) : textResp(404, 'not found');
		}
		return textResp(404, 'unhandled url: ' + url);
	}

	class Plugin {
		constructor(a) { this.app = a; }
		loadData() { return Promise.resolve({}); }
		saveData() { return Promise.resolve(); }
		registerObsidianProtocolHandler() {}
		registerView() {}
		registerEvent() {}
		addRibbonIcon() {}
		addCommand() {}
		addSettingTab() {}
	}
	class PluginSettingTab { update() {} }
	class ItemView {}
	class Modal {}
	const notices = [];
	class Notice {
		constructor(msg) {
			notices.push(msg);
		}
	}

	return {
		app,
		vault,
		files,
		driveStore,
		notices,
		emitVault,
		setVaultFile,
		setActiveFile: (p) => {
			const rec = files.get(p);
			activeFile = rec ? toTFile(p, rec) : new TFile(p, '');
		},
		obsidianMock: {
			Plugin,
			PluginSettingTab,
			Setting: class {},
			ItemView,
			Modal,
			TFile,
			WorkspaceLeaf: class {},
			Notice,
			Platform: { isIosApp: false },
			normalizePath: norm,
			requestUrl,
		},
	};
}

async function runTargetWorker(target, mainJsPath, simulatedHttpMs) {
	await import('fake-indexeddb/auto');
	globalThis.window = globalThis;
	const {
		app,
		files,
		driveStore,
		notices,
		emitVault,
		setVaultFile,
		setActiveFile,
		obsidianMock,
	} = makeMockObsidian(simulatedHttpMs);

	const cjsRequire = (id) => {
		if (id === 'obsidian') return obsidianMock;
		throw new Error(`Unexpected require(${id}) in plugin bundle`);
	};
	const cjsModule = { exports: {} };
	const code = fs.readFileSync(mainJsPath, 'utf8');

	const t0 = performance.now();
	const wrapper = vm.compileFunction(
		code,
		['exports', 'require', 'module', '__filename', '__dirname'],
		{ filename: mainJsPath },
	);
	wrapper(cjsModule.exports, cjsRequire, cjsModule, mainJsPath, path.dirname(mainJsPath));
	const PluginClass = cjsModule.exports.default;
	const requireAndDecodeMs = performance.now() - t0;

	const plugin = new PluginClass(app, { id: 'test-gdoc', version: '0.1.0' });
	const tOnload = performance.now();
	await plugin.onload();
	const onloadMs = performance.now() - tOnload;

	const tInit = performance.now();
	await plugin.ensureBridgeConfigured();
	const coldInitMs = performance.now() - tInit;

	// 1. Pull 40 remote files (~3.2 KB each)
	const tPull = performance.now();
	await plugin.pullDriveFolder();
	const pull40FilesMs = performance.now() - tPull;

	if (files.get('test-gdoc-sync/note-000.md')?.content !== driveStore.get('f-0').content) {
		throw new Error(`${target}: pulled file mismatch; notices=${JSON.stringify(notices)}`);
	}

	// 2. Startup / incremental Vault scan check
	let seedOrScanMs = 0;
	let idbBench100Ms = null;
	let renameVerified = null;

	if (typeof plugin.seedLocalBaseSnapshots === 'function') {
		for (let i = 0; i < 40; i++) {
			files.delete(`test-gdoc-base/note-${String(i).padStart(3, '0')}.md`);
		}
		const tSeed = performance.now();
		await plugin.seedLocalBaseSnapshots();
		seedOrScanMs = performance.now() - tSeed;
	} else {
		// On efficiency-hacks: scan 40 synced files (must re-hash only if mtime changed once, then 0 hashes on repeat).
		await plugin.wasmBridge.getSyncState({
			driveFolderPath: '/test-gdoc',
			localSyncFolderPath: 'test-gdoc-sync',
			includeDrive: false,
		});
		const tScan = performance.now();
		const res2 = await plugin.wasmBridge.getSyncState({
			driveFolderPath: '/test-gdoc',
			localSyncFolderPath: 'test-gdoc-sync',
			includeDrive: false,
		});
		seedOrScanMs = performance.now() - tScan;
		if (res2.hashedVaultFiles !== 0) {
			throw new Error(`expected 0 hashed files on second scan, got ${res2.hashedVaultFiles}`);
		}

		// Verify Vault delete + create rename detection via SHA-256 + IndexedDB MigrateKey (no vault.on('rename')).
		const oldPath = 'test-gdoc-sync/note-001.md';
		const oldRec = files.get(oldPath);
		const oldTFile = app.vault.getFileByPath(oldPath);
		files.delete(oldPath);
		emitVault('delete', oldTFile);
		const newPath = 'test-gdoc-sync/note-001-renamed.md';
		const newTFile = setVaultFile(newPath, oldRec.content);
		emitVault('create', newTFile);

		await plugin.flushVaultHints();
		const renameScan = await plugin.wasmBridge.getSyncState({
			driveFolderPath: '/test-gdoc',
			localSyncFolderPath: 'test-gdoc-sync',
			includeDrive: false,
		});
		const renamedRec = renameScan.records.find((r) => r.fileName === 'note-001-renamed.md');
		if (!renamedRec || renamedRec.relation !== 'localRenamed' || renamedRec.driveFileId !== 'f-1' || !renamedRec.hasBase) {
			throw new Error(`Vault delete+create rename detection failed: ${JSON.stringify(renamedRec)}`);
		}
		// Push renamed note so Drive follows rename too.
		setActiveFile(newPath);
		await plugin.pushActiveNote();
		if (driveStore.get('f-1').name !== 'note-001-renamed.md') {
			throw new Error(`Drive rename propagation failed: ${driveStore.get('f-1').name}`);
		}
		renameVerified = true;

		// Run real go-indexeddb 100-record write/scan/get/clear benchmark.
		const idbBench = await plugin.wasmBridge.runIdbBenchmark(100);
		idbBench100Ms = +(idbBench.writeMs + idbBench.scanMs + idbBench.getMs + idbBench.clearMs).toFixed(2);
	}

	// 3. Push active note (10 clean merges: edit line 1 locally while remote edited last line)
	const targetPath = 'test-gdoc-sync/note-000.md';
	const tPush = performance.now();
	for (let i = 0; i < 10; i++) {
		const baseText = `line-0-${i}\nline-1\nline-2\nline-3`;
		if (typeof plugin.wasmBridge.setBaseSnapshot === 'function') {
			await plugin.wasmBridge.setBaseSnapshot('note-000.md', baseText);
		} else {
			setVaultFile('test-gdoc-base/note-000.md', baseText);
		}
		setVaultFile(targetPath, `local-0-${i}\nline-1\nline-2\nline-3`);
		setActiveFile(targetPath);
		driveStore.get('f-0').content = `line-0-${i}\nline-1\nline-2\nremote-3-${i}`;
		await plugin.pushActiveNote();
		if (files.get(targetPath)?.content !== `local-0-${i}\nline-1\nline-2\nremote-3-${i}`) {
			throw new Error(`${target}: 3-way clean merge mismatch at iter ${i}`);
		}
	}
	const push10CleanMs = performance.now() - tPush;

	// 4. Direct synchronous merge compute benchmark
	let merge200Ms = null;
	if (typeof plugin.wasmBridge.reconcileTextDirect === 'function') {
		const b = 'line 1\nline 2\nline 3\nline 4\n'.repeat(25);
		const l = 'local 1\nline 2\nline 3\nline 4\n' + 'line 1\nline 2\nline 3\nline 4\n'.repeat(24);
		const r = 'line 1\nline 2\nline 3\nremote 4\n' + 'line 1\nline 2\nline 3\nline 4\n'.repeat(24);
		const tMerge = performance.now();
		for (let i = 0; i < 200; i++) {
			const res = await plugin.wasmBridge.reconcileTextDirect(b, l, r);
			if (res.outcome !== 'clean') throw new Error('unexpected merge outcome');
		}
		merge200Ms = performance.now() - tMerge;
	}

	const mem = process.memoryUsage();
	plugin.onunload();

	return {
		target,
		simulatedHttpMs,
		requireAndDecodeMs: +requireAndDecodeMs.toFixed(2),
		onloadMs: +onloadMs.toFixed(2),
		coldInitMs: +coldInitMs.toFixed(2),
		bridgeInitMs: plugin.wasmBridge.initDurationMs,
		pull40FilesMs: +pull40FilesMs.toFixed(2),
		scanOrSeed40Ms: +seedOrScanMs.toFixed(2),
		push10CleanMs: +push10CleanMs.toFixed(2),
		merge200DirectMs: merge200Ms !== null ? +merge200Ms.toFixed(2) : null,
		idbBench100Ms,
		renameOk: renameVerified,
		rssMB: +(mem.rss / (1024 * 1024)).toFixed(1),
		heapUsedMB: +(mem.heapUsed / (1024 * 1024)).toFixed(1),
	};
}

function runWorkerSubprocess(target, mainJsPath, simulatedHttpMs) {
	const out = execFileSync(
		process.execPath,
		[path.join(ROOT, 'benchmarks', 'run-bench.mjs'), '--worker', target, mainJsPath, String(simulatedHttpMs)],
		{ encoding: 'utf8' },
	);
	const marker = '__BENCH_JSON__';
	const line = out.split('\n').find((l) => l.startsWith(marker));
	if (!line) {
		throw new Error(`No benchmark JSON from ${target}:\n${out}`);
	}
	return JSON.parse(line.slice(marker.length));
}

if (process.argv[2] === '--worker') {
	const [, , , target, mainJsPath, httpMsStr] = process.argv;
	const origWarn = console.warn;
	console.warn = () => {};
	const res = await runTargetWorker(target, mainJsPath, Number(httpMsStr));
	console.warn = origWarn;
	console.log('__BENCH_JSON__' + JSON.stringify(res));
	process.exit(0);
}

const sizes = measureArtifacts();
console.log('\n=== Artifact Size Comparison (baseline-main vs efficiency-hacks + go-indexeddb) ===');
console.table(
	sizes.map((s) => ({
		Artifact: s.name,
		'main (B)': s.baseRaw,
		'new (B)': s.curRaw,
		'delta (B)': `${s.deltaRaw >= 0 ? '+' : ''}${s.deltaRaw}`,
		'main gz (B)': s.baseGz,
		'new gz (B)': s.curGz,
		'delta gz': `${s.deltaGz >= 0 ? '+' : ''}${s.deltaGz}`,
	})),
);

for (const httpMs of [0, 3]) {
	const base = runWorkerSubprocess('baseline-main', path.join(BASELINE_DIR, 'main.js'), httpMs);
	const cur = runWorkerSubprocess('efficiency-hacks', path.join(ROOT, 'main.js'), httpMs);
	console.log(`\n=== Runtime Benchmark (Node ${process.version} + fake-indexeddb, simulated HTTP RTT = ${httpMs} ms) ===`);
	console.table([base, cur]);
}
