/**
 * Strict runtime type-narrowing assertions for JSON payloads returned by Go Wasm RPCs.
 */

import type {
	BaseMeta,
	BaseSnapshotResult,
	ConfigureResult,
	DriveFileState,
	FileMeta,
	GetSyncStateResult,
	IDBBenchResult,
	ListFilesResult,
	PullPrepareResult,
	PullSummary,
	PushApplySummary,
	PushExecuteResult,
	ReadFileResult,
	SyncRecord,
	VaultFileState,
	WriteFileResult,
} from '../settings.generated';

function asRecord(value: unknown, label: string): Record<string, unknown> {
	if (typeof value !== 'object' || value === null) {
		throw new Error(`Go Wasm returned a non-object ${label}`);
	}
	return value as Record<string, unknown>;
}

/** Validates a `FileMeta` payload from Go. */
export function assertFileMeta(value: unknown): asserts value is FileMeta {
	const r = asRecord(value, 'FileMeta');
	if (
		typeof r.id !== 'string' ||
		typeof r.name !== 'string' ||
		typeof r.mimeType !== 'string' ||
		typeof r.modifiedTime !== 'string' ||
		typeof r.size !== 'string' ||
		typeof r.sha256Checksum !== 'string' ||
		typeof r.md5Checksum !== 'string'
	) {
		throw new Error('Go Wasm returned an unexpected FileMeta shape');
	}
}

/** Validates a `ConfigureResult` payload from Go. */
export function assertConfigureResult(value: unknown): asserts value is ConfigureResult {
	const r = asRecord(value, 'ConfigureResult');
	if (typeof r.configured !== 'boolean' || typeof r.maxPendingRequests !== 'number') {
		throw new Error('Go Wasm returned an unexpected ConfigureResult shape');
	}
}

/** Validates a `ListFilesResult` payload from Go. */
export function assertListFilesResult(value: unknown): asserts value is ListFilesResult {
	const r = asRecord(value, 'ListFilesResult');
	if (
		typeof r.folderId !== 'string' ||
		!Array.isArray(r.files) ||
		typeof r.rotatedRefreshToken !== 'string'
	) {
		throw new Error('Go Wasm returned an unexpected ListFilesResult shape');
	}
	for (const f of r.files) {
		assertFileMeta(f);
	}
}

/** Validates a `ReadFileResult` payload from Go. */
export function assertReadFileResult(value: unknown): asserts value is ReadFileResult {
	const r = asRecord(value, 'ReadFileResult');
	assertFileMeta(r.file);
	if (typeof r.content !== 'string' || typeof r.rotatedRefreshToken !== 'string') {
		throw new Error('Go Wasm returned an unexpected ReadFileResult shape');
	}
}

/** Validates a `WriteFileResult` payload from Go. */
export function assertWriteFileResult(value: unknown): asserts value is WriteFileResult {
	const r = asRecord(value, 'WriteFileResult');
	assertFileMeta(r.file);
	if (typeof r.rotatedRefreshToken !== 'string') {
		throw new Error('Go Wasm returned an unexpected WriteFileResult shape');
	}
}

/** Validates a `PullPrepareResult` payload from Go. */
export function assertPullPrepareResult(value: unknown): asserts value is PullPrepareResult {
	const r = asRecord(value, 'PullPrepareResult');
	if (
		typeof r.batchId !== 'number' ||
		typeof r.pulledCount !== 'number' ||
		typeof r.rotatedRefreshToken !== 'string'
	) {
		throw new Error('Go Wasm returned an unexpected PullPrepareResult shape');
	}
}

/** Validates a `PullSummary` payload from Go. */
export function assertPullSummary(value: unknown): asserts value is PullSummary {
	const r = asRecord(value, 'PullSummary');
	if (
		typeof r.pulled !== 'number' ||
		typeof r.updatedIdb !== 'number' ||
		typeof r.renamed !== 'number'
	) {
		throw new Error('Go Wasm returned an unexpected PullSummary shape');
	}
}

/** Validates a `PushExecuteResult` payload from Go. */
export function assertPushExecuteResult(value: unknown): asserts value is PushExecuteResult {
	const r = asRecord(value, 'PushExecuteResult');
	if (typeof r.outcome !== 'string' || typeof r.rotatedRefreshToken !== 'string') {
		throw new Error('Go Wasm returned an unexpected PushExecuteResult shape');
	}
	assertFileMeta(r.file);
}

/** Validates a `PushApplySummary` payload from Go. */
export function assertPushApplySummary(value: unknown): asserts value is PushApplySummary {
	const r = asRecord(value, 'PushApplySummary');
	if (typeof r.outcome !== 'string' || typeof r.conflictCopyName !== 'string') {
		throw new Error('Go Wasm returned an unexpected PushApplySummary shape');
	}
}

/** Validates a `prepareVaultScan` response from Go. */
export function assertPrepareScanResponse(
	value: unknown,
): asserts value is { scanId: number; pathsToRead: string[] } {
	const r = asRecord(value, 'prepareScanResponse');
	if (
		typeof r.scanId !== 'number' ||
		!Array.isArray(r.pathsToRead) ||
		r.pathsToRead.some((p) => typeof p !== 'string')
	) {
		throw new Error('Go Wasm returned an unexpected prepareVaultScan shape');
	}
}

function assertDriveFileState(value: unknown): asserts value is DriveFileState {
	const r = asRecord(value, 'DriveFileState');
	if (
		typeof r.fileId !== 'string' ||
		typeof r.name !== 'string' ||
		typeof r.mimeType !== 'string' ||
		typeof r.modifiedTime !== 'string' ||
		typeof r.size !== 'number' ||
		typeof r.sha256Checksum !== 'string' ||
		typeof r.md5Checksum !== 'string'
	) {
		throw new Error('Go Wasm returned an unexpected DriveFileState shape');
	}
}

function assertVaultFileState(value: unknown): asserts value is VaultFileState {
	const r = asRecord(value, 'VaultFileState');
	if (
		typeof r.path !== 'string' ||
		typeof r.size !== 'number' ||
		typeof r.mtimeMs !== 'number' ||
		typeof r.sha256 !== 'string' ||
		typeof r.md5 !== 'string' ||
		typeof r.deletedAtMs !== 'number'
	) {
		throw new Error('Go Wasm returned an unexpected VaultFileState shape');
	}
}

function assertBaseMeta(value: unknown): asserts value is BaseMeta {
	const r = asRecord(value, 'BaseMeta');
	if (
		typeof r.sha256 !== 'string' ||
		typeof r.md5 !== 'string' ||
		typeof r.size !== 'number'
	) {
		throw new Error('Go Wasm returned an unexpected BaseMeta shape');
	}
}

function assertSyncRecord(value: unknown): asserts value is SyncRecord {
	const r = asRecord(value, 'SyncRecord');
	if (
		typeof r.fileName !== 'string' ||
		typeof r.driveFileId !== 'string' ||
		typeof r.hasBase !== 'boolean' ||
		typeof r.relation !== 'string' ||
		typeof r.renamedFrom !== 'string' ||
		typeof r.updatedAt !== 'string'
	) {
		throw new Error('Go Wasm returned an unexpected SyncRecord shape');
	}
	if (r.drive !== null) {
		assertDriveFileState(r.drive);
	}
	if (r.vault !== null) {
		assertVaultFileState(r.vault);
	}
	if (r.baseMeta !== null) {
		assertBaseMeta(r.baseMeta);
	}
}

/** Validates a `GetSyncStateResult` payload from Go. */
export function assertGetSyncStateResult(value: unknown): asserts value is GetSyncStateResult {
	const r = asRecord(value, 'GetSyncStateResult');
	if (
		!Array.isArray(r.records) ||
		typeof r.renamedVaultFiles !== 'number' ||
		typeof r.renamedDriveFiles !== 'number' ||
		typeof r.hashedVaultFiles !== 'number' ||
		typeof r.rotatedRefreshToken !== 'string'
	) {
		throw new Error('Go Wasm returned an unexpected GetSyncStateResult shape');
	}
	for (const rec of r.records) {
		assertSyncRecord(rec);
	}
}

/** Validates a `BaseSnapshotResult` payload from Go. */
export function assertBaseSnapshotResult(value: unknown): asserts value is BaseSnapshotResult {
	const r = asRecord(value, 'BaseSnapshotResult');
	if (
		typeof r.fileName !== 'string' ||
		typeof r.hasBase !== 'boolean' ||
		typeof r.sha256 !== 'string' ||
		typeof r.content !== 'string'
	) {
		throw new Error('Go Wasm returned an unexpected BaseSnapshotResult shape');
	}
}

/** Validates an `IDBBenchResult` payload from Go. */
export function assertIDBBenchResult(value: unknown): asserts value is IDBBenchResult {
	const r = asRecord(value, 'IDBBenchResult');
	if (
		typeof r.count !== 'number' ||
		typeof r.writeMs !== 'number' ||
		typeof r.scanMs !== 'number' ||
		typeof r.getMs !== 'number' ||
		typeof r.clearMs !== 'number'
	) {
		throw new Error('Go Wasm returned an unexpected IDBBenchResult shape');
	}
}
