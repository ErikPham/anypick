import { createHash, randomUUID } from 'node:crypto';
import { open, chmod, mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import type { AnyPickApp } from '../core/app';
import { MAX_BACKUP_ACCOUNTS, MAX_BACKUP_ENCODED_BYTES } from '../core/account-backup';
import type { AccountRestoreInspection } from '../core/account-restore';
import { AnyPickError, isAnyPickError } from '../utils/errors';
import type {
  TrayBackupDisposition,
  TrayBackupPreviewEntry,
  TrayBackupPreviewResult,
  TrayBackupReason,
} from './protocol';

const MAX_INSPECTIONS = 8;
const INSPECTION_TTL_MS = 10 * 60 * 1000;
const COPY_CHUNK_BYTES = 64 * 1024;

interface StoredPreviewEntry {
  id: string;
  provider: string;
  name: string;
  label: string;
  disposition: TrayBackupDisposition;
  reason?: TrayBackupReason;
}

interface InspectionRecord {
  stagedPath: string;
  stagedRoot: string;
  digest: string;
  expiresAt: number;
  createdAt: string;
  entries: StoredPreviewEntry[];
  expiryTimer?: NodeJS.Timeout;
}

type BackupAccountService = Pick<
  AnyPickApp['accounts'],
  'inspectAccountBackup' | 'restoreAccountBackup'
>;

export interface ReadyBackupPreview {
  inspectionId: string;
  createdAt: string;
  entries: TrayBackupPreviewEntry[];
}

/** Process-local capability store binding a visible preview to privately staged backup bytes. */
export class TrayBackupInspectionManager {
  readonly #records = new Map<string, InspectionRecord>();

  constructor(private readonly now: () => number = Date.now) {}

  async inspect(service: BackupAccountService, path: string): Promise<ReadyBackupPreview> {
    const staged = await stageBackup(path);
    try {
      const inspection = await service.inspectAccountBackup(staged.path);
      const entries = storedEntries(inspection);
      const inspectionId = randomUUID();
      await this.#purgeExpired();
      while (this.#records.size >= MAX_INSPECTIONS) {
        const oldest = this.#records.keys().next().value;
        if (oldest === undefined) {
          break;
        }
        await this.#deleteRecord(oldest);
      }
      const record: InspectionRecord = {
        stagedPath: staged.path,
        stagedRoot: staged.root,
        digest: staged.digest,
        expiresAt: this.now() + INSPECTION_TTL_MS,
        createdAt: inspection.createdAt,
        entries,
      };
      record.expiryTimer = setTimeout(() => {
        if (this.#records.get(inspectionId) === record) {
          this.#records.delete(inspectionId);
          void cleanupStage(record.stagedRoot);
        }
      }, INSPECTION_TTL_MS);
      record.expiryTimer.unref();
      this.#records.set(inspectionId, record);
      return {
        inspectionId,
        createdAt: inspection.createdAt,
        entries: entries.map(publicEntry),
      };
    } catch (err) {
      await cleanupStage(staged.root);
      throw err;
    }
  }

  async restore(
    service: BackupAccountService,
    inspectionId: string,
    entryIds: string[],
    overwriteConfirmed: boolean,
  ): Promise<number> {
    const record = await this.#record(inspectionId);
    const selectedIds = uniqueSelection(entryIds);
    const byId = new Map(record.entries.map((entry) => [entry.id, entry]));
    const selected = selectedIds.map((id) => {
      const entry = byId.get(id);
      if (!entry || entry.disposition === 'blocked' || entry.disposition === 'unavailable') {
        throw inspectionError('The backup selection is no longer available.');
      }
      return entry;
    });
    const replacements = selected.filter((entry) => entry.disposition === 'replace');
    if (replacements.length > 0 && !overwriteConfirmed) {
      throw inspectionError('Replacing selected accounts requires confirmation.');
    }

    this.#records.delete(inspectionId);
    if (record.expiryTimer) {
      clearTimeout(record.expiryTimer);
    }
    try {
      if ((await hashBoundedFile(record.stagedPath)) !== record.digest) {
        throw inspectionError('The staged backup failed its integrity check.');
      }
      const current = await service.inspectAccountBackup(record.stagedPath);
      if (!samePlan(record, current)) {
        throw inspectionError('The backup restore plan has changed.');
      }

      const refs = selected.map(({ provider, name }) => ({ provider, name }));
      const replace = replacements.map(({ provider, name }) => ({ provider, name }));
      const restored = await service.restoreAccountBackup(record.stagedPath, refs, { replace });
      return restored.length;
    } finally {
      await cleanupStage(record.stagedRoot);
    }
  }

  errorResult(requestId: string, error?: unknown): TrayBackupPreviewResult {
    const reason = inspectionFailureReason(error);
    return {
      version: 1,
      requestId,
      status: 'error',
      error: 'inspection-failed',
      code: 'BACKUP_INSPECT_FAILED',
      reason,
      message: inspectionFailureMessage(reason),
    };
  }

  async #record(inspectionId: string): Promise<InspectionRecord> {
    await this.#purgeExpired();
    const record = this.#records.get(inspectionId);
    if (!record) {
      throw inspectionError('The backup inspection is unknown or expired.');
    }
    return record;
  }

  async #purgeExpired(): Promise<void> {
    const now = this.now();
    for (const [id, record] of this.#records) {
      if (record.expiresAt <= now) {
        await this.#deleteRecord(id);
      }
    }
  }

  async #deleteRecord(id: string): Promise<void> {
    const record = this.#records.get(id);
    this.#records.delete(id);
    if (record) {
      if (record.expiryTimer) {
        clearTimeout(record.expiryTimer);
      }
      await cleanupStage(record.stagedRoot);
    }
  }
}

function inspectionFailureReason(
  error: unknown,
): 'damaged' | 'newer-version' | 'permission-denied' {
  if (isAnyPickError(error) && error.code === 'BACKUP_VERSION') {
    return 'newer-version';
  }
  if (isAnyPickError(error) && error.code === 'BACKUP_MISSING') {
    return 'permission-denied';
  }
  if (typeof error === 'object' && error !== null && 'code' in error) {
    const code = String(error.code);
    if (code === 'EACCES' || code === 'EPERM' || code === 'ENOENT') {
      return 'permission-denied';
    }
  }
  return 'damaged';
}

function inspectionFailureMessage(
  reason: 'damaged' | 'newer-version' | 'permission-denied',
): string {
  switch (reason) {
    case 'newer-version':
      return 'This backup was created by a newer AnyPick version. Update AnyPick, then review it again.';
    case 'permission-denied':
      return 'AnyPick does not have permission to read that backup. Choose another file or update its permissions.';
    case 'damaged':
      return 'AnyPick could not inspect that backup. Choose a valid AnyPick backup file and try again.';
  }
}

function storedEntries(inspection: AccountRestoreInspection): StoredPreviewEntry[] {
  if (inspection.entries.length === 0 || inspection.entries.length > MAX_BACKUP_ACCOUNTS) {
    throw inspectionError('The backup preview has an invalid number of entries.');
  }
  return inspection.entries.map((entry, index) => {
    if (!boundedRef(entry.provider, 64) || !boundedRef(entry.name, 128)) {
      throw inspectionError('The backup preview contains an invalid account reference.');
    }
    return {
      id: index.toString(36),
      provider: entry.provider,
      name: entry.name,
      label: boundedDisplay(entry.label, entry.name),
      disposition: entry.disposition,
      ...(entry.reason ? { reason: entry.reason } : {}),
    };
  });
}

function publicEntry(entry: StoredPreviewEntry): TrayBackupPreviewEntry {
  return {
    id: entry.id,
    providerId: entry.provider,
    name: entry.name,
    label: entry.label,
    disposition: entry.disposition,
    ...(entry.reason ? { reason: entry.reason } : {}),
  };
}

function boundedDisplay(value: string, fallback: string): string {
  const cleaned = value.replace(/\p{Cc}/gu, ' ').trim();
  return (cleaned || fallback).slice(0, 160);
}

function boundedRef(value: string, maxLength: number): boolean {
  return value.length > 0 && value.length <= maxLength && !/\p{Cc}/u.test(value);
}

function uniqueSelection(entryIds: string[]): string[] {
  if (entryIds.length === 0 || entryIds.length > 64 || new Set(entryIds).size !== entryIds.length) {
    throw inspectionError('The backup selection is invalid.');
  }
  return entryIds;
}

function samePlan(record: InspectionRecord, current: AccountRestoreInspection): boolean {
  if (record.createdAt !== current.createdAt || record.entries.length !== current.entries.length) {
    return false;
  }
  return record.entries.every((entry, index) => {
    const next = current.entries[index];
    return (
      next?.provider === entry.provider &&
      next.name === entry.name &&
      next.disposition === entry.disposition &&
      next.reason === entry.reason
    );
  });
}

async function stageBackup(sourcePath: string): Promise<{
  root: string;
  path: string;
  digest: string;
}> {
  const root = await mkdtemp(join(tmpdir(), 'anypick-backup-inspection-'));
  const stagedPath = join(root, 'backup.anypick-backup');
  try {
    await chmod(root, 0o700);
    const source = await open(sourcePath, 'r');
    try {
      const before = await source.stat({ bigint: true });
      if (!before.isFile() || before.size > BigInt(MAX_BACKUP_ENCODED_BYTES)) {
        throw inspectionError('The selected backup is not a bounded regular file.');
      }

      const destination = await open(stagedPath, 'wx', 0o600);
      try {
        const hash = createHash('sha256');
        const buffer = Buffer.allocUnsafe(COPY_CHUNK_BYTES);
        let bytes = 0;
        while (true) {
          const remaining = MAX_BACKUP_ENCODED_BYTES - bytes;
          const length = Math.min(buffer.length, remaining + 1);
          const { bytesRead } = await source.read(buffer, 0, length, null);
          if (bytesRead === 0) {
            break;
          }
          bytes += bytesRead;
          if (bytes > MAX_BACKUP_ENCODED_BYTES) {
            throw inspectionError('The selected backup exceeds the size limit.');
          }
          const chunk = buffer.subarray(0, bytesRead);
          hash.update(chunk);
          await writeAll(destination, chunk);
        }
        await destination.sync();

        const after = await source.stat({ bigint: true });
        if (
          !after.isFile() ||
          bytes !== Number(before.size) ||
          before.dev !== after.dev ||
          before.ino !== after.ino ||
          before.size !== after.size ||
          before.mtimeNs !== after.mtimeNs ||
          before.ctimeNs !== after.ctimeNs
        ) {
          throw inspectionError('The selected backup changed while it was staged.');
        }
        return { root, path: stagedPath, digest: hash.digest('hex') };
      } finally {
        await destination.close();
      }
    } finally {
      await source.close();
    }
  } catch (err) {
    await cleanupStage(root);
    throw err;
  }
}

async function writeAll(
  destination: Awaited<ReturnType<typeof open>>,
  chunk: Buffer,
): Promise<void> {
  let offset = 0;
  while (offset < chunk.length) {
    const { bytesWritten } = await destination.write(chunk, offset, chunk.length - offset, null);
    if (bytesWritten === 0) {
      throw inspectionError('The selected backup could not be staged.');
    }
    offset += bytesWritten;
  }
}

async function hashBoundedFile(path: string): Promise<string> {
  const file = await open(path, 'r');
  try {
    const stat = await file.stat();
    if (!stat.isFile() || stat.size > MAX_BACKUP_ENCODED_BYTES) {
      throw inspectionError('The staged backup failed its integrity check.');
    }
    const hash = createHash('sha256');
    const buffer = Buffer.allocUnsafe(COPY_CHUNK_BYTES);
    let bytes = 0;
    while (true) {
      const remaining = MAX_BACKUP_ENCODED_BYTES - bytes;
      const { bytesRead } = await file.read(
        buffer,
        0,
        Math.min(buffer.length, remaining + 1),
        null,
      );
      if (bytesRead === 0) {
        break;
      }
      bytes += bytesRead;
      if (bytes > MAX_BACKUP_ENCODED_BYTES) {
        throw inspectionError('The staged backup failed its integrity check.');
      }
      hash.update(buffer.subarray(0, bytesRead));
    }
    if (bytes !== stat.size) {
      throw inspectionError('The staged backup failed its integrity check.');
    }
    return hash.digest('hex');
  } finally {
    await file.close();
  }
}

async function cleanupStage(root: string): Promise<void> {
  await rm(root, { recursive: true, force: true }).catch(() => {});
}

function inspectionError(message: string): AnyPickError {
  return new AnyPickError(message, { code: 'BACKUP_INSPECTION_INVALID', mutated: false });
}
