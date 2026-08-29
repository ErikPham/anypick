import { mkdtemp, open, rm } from 'node:fs/promises';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import type { AccountMeta, AccountProxyConfig } from '../types';
import { DEFAULT_PROXY_CONFIG } from '../types';
import { AnyPickError, isAnyPickError } from '../utils/errors';
import { normalizeAccountName } from '../utils/slug';
import {
  ACCOUNT_BACKUP_KIND,
  ACCOUNT_BACKUP_VERSION,
  MAX_BACKUP_ACCOUNTS,
  MAX_BACKUP_DECODED_BYTES,
  MAX_BACKUP_ENCODED_BYTES,
  MAX_BACKUP_FILES,
  type SavedAccountBackupEntry,
} from './account-backup';
import { decodeAccountEnvelope, type DecodedAccountEnvelope } from './account-codec';
import { writeSnapshotFiles } from './account-transfer';
import type { ProviderRegistry } from './registry';
import type { AccountBatchWrite, AccountStore } from './store';

export type AccountRestoreDisposition = 'create' | 'replace' | 'blocked' | 'unavailable';
export type AccountRestoreReason = 'identity-conflict' | 'provider-unavailable';

export interface AccountRestorePreviewEntry {
  provider: string;
  name: string;
  label: string;
  disposition: AccountRestoreDisposition;
  reason?: AccountRestoreReason;
}

export interface AccountRestoreInspection {
  createdAt: string;
  entries: AccountRestorePreviewEntry[];
}

export interface AccountRestoreOptions {
  /** Exact existing provider/name pairs the caller explicitly confirmed replacing. */
  replace?: SavedAccountBackupEntry[];
}

interface DecodedPackageEntry {
  provider: string;
  name: string;
  envelope: DecodedAccountEnvelope;
}

interface PlannedEntry extends DecodedPackageEntry {
  label: string;
  identity?: string;
  disposition: AccountRestoreDisposition;
  reason?: AccountRestoreReason;
}

interface PreparedPackage {
  createdAt: string;
  entries: PlannedEntry[];
}

export async function inspectAccountBackup(
  store: AccountStore,
  registry: ProviderRegistry,
  path: string,
): Promise<AccountRestoreInspection> {
  const prepared = await prepareAccountBackup(store, registry, path);
  return {
    createdAt: prepared.createdAt,
    entries: prepared.entries.map((entry) => previewEntry(entry)),
  };
}

export async function restoreSelectedAccountBackup(
  store: AccountStore,
  registry: ProviderRegistry,
  path: string,
  selected: SavedAccountBackupEntry[],
  opts: AccountRestoreOptions = {},
): Promise<AccountMeta[]> {
  const prepared = await prepareAccountBackup(store, registry, path);
  const selectedKeys = validateRefs(selected, 'BACKUP_SELECTION_INVALID');
  const replacementKeys = validateRefs(opts.replace ?? [], 'BACKUP_REPLACE_INVALID', true);
  for (const key of replacementKeys) {
    if (!selectedKeys.has(key)) {
      throw restoreError(
        'Replacement confirmation does not match the selected accounts.',
        'BACKUP_REPLACE_INVALID',
      );
    }
  }

  const byKey = new Map(prepared.entries.map((entry) => [entryKey(entry), entry]));
  const chosen = [...selectedKeys].map((key) => {
    const entry = byKey.get(key);
    if (!entry) {
      throw restoreError(
        'The restore selection contains an unknown account.',
        'BACKUP_SELECTION_INVALID',
      );
    }
    if (entry.disposition === 'blocked') {
      throw restoreError(
        'A selected account has a different-name identity collision.',
        'BACKUP_COLLISION',
      );
    }
    if (entry.disposition === 'unavailable') {
      throw restoreError(
        'A selected account cannot be prepared by its provider.',
        'BACKUP_UNAVAILABLE',
      );
    }
    if (entry.disposition === 'replace' && !replacementKeys.has(key)) {
      throw restoreError(
        'Replacing an existing account requires explicit confirmation.',
        'BACKUP_REPLACE_REQUIRED',
      );
    }
    return entry;
  });

  const stage = await mkdtemp(join(tmpdir(), 'anypick-restore-'));
  try {
    const now = new Date().toISOString();
    const writes: AccountBatchWrite[] = [];
    const metas: AccountMeta[] = [];
    for (const [index, entry] of chosen.entries()) {
      const snapshotDir = join(stage, String(index));
      await writeSnapshotFiles(snapshotDir, entry.envelope.files);
      const existing = await store.getAccount(entry.provider, entry.name);
      const meta: AccountMeta = {
        name: entry.name,
        provider: entry.provider,
        createdAt: existing?.meta.createdAt ?? entry.envelope.meta.createdAt ?? now,
        updatedAt: now,
        label: entry.label,
        identity: entry.identity,
        notes: entry.envelope.meta.notes,
        credentialKind: entry.envelope.meta.credentialKind,
        credentialClass: entry.envelope.meta.credentialClass,
      };
      const proxy: AccountProxyConfig = {
        ...DEFAULT_PROXY_CONFIG,
        ...entry.envelope.proxy,
        enabled: false,
        options: undefined,
      };
      writes.push({ meta, proxy, snapshotDir });
      metas.push(meta);
    }
    await store.writeAccountsBatch(writes);
    return metas;
  } finally {
    await rm(stage, { recursive: true, force: true }).catch(() => {});
  }
}

async function prepareAccountBackup(
  store: AccountStore,
  registry: ProviderRegistry,
  path: string,
): Promise<PreparedPackage> {
  const decoded = await readBackupPackage(path, registry);
  const localByProvider = new Map(
    await Promise.all(
      registry
        .list()
        .map(async (provider) => [provider.id, await store.listAccounts(provider.id)] as const),
    ),
  );
  const described: Array<{
    entry: DecodedPackageEntry;
    label: string;
    identity?: string;
    unavailable: boolean;
  }> = [];
  const stage = await mkdtemp(join(tmpdir(), 'anypick-inspect-'));
  try {
    for (const [index, entry] of decoded.entries.entries()) {
      const provider = registry.get(entry.provider);
      let label = entry.envelope.meta.label ?? entry.name;
      let identity = entry.envelope.meta.identity;
      let unavailable = false;
      if (provider.describeSnapshot) {
        const snapshotDir = join(stage, String(index));
        await writeSnapshotFiles(snapshotDir, entry.envelope.files);
        try {
          const details = await provider.describeSnapshot(snapshotDir);
          label = details.label ?? label;
          identity = details.identity ?? identity;
        } catch {
          unavailable = true;
        }
      }
      described.push({ entry, label, identity, unavailable });
    }
  } finally {
    await rm(stage, { recursive: true, force: true }).catch(() => {});
  }

  const packageIdentityCounts = new Map<string, number>();
  for (const item of described) {
    if (item.identity) {
      const key = identityKey(item.entry.provider, item.identity);
      packageIdentityCounts.set(key, (packageIdentityCounts.get(key) ?? 0) + 1);
    }
  }

  const entries = described.map(({ entry, label, identity, unavailable }): PlannedEntry => {
    if (unavailable) {
      return {
        provider: entry.provider,
        name: entry.name,
        envelope: entry.envelope,
        label,
        identity,
        disposition: 'unavailable',
        reason: 'provider-unavailable',
      };
    }
    const local = localByProvider.get(entry.provider) ?? [];
    const identityCollision = identity
      ? local.some(
          (account) =>
            account.meta.name !== entry.name &&
            account.meta.identity !== undefined &&
            normalizedIdentity(account.meta.identity) === normalizedIdentity(identity),
        ) || (packageIdentityCounts.get(identityKey(entry.provider, identity)) ?? 0) > 1
      : false;
    if (identityCollision) {
      return {
        provider: entry.provider,
        name: entry.name,
        envelope: entry.envelope,
        label,
        identity,
        disposition: 'blocked',
        reason: 'identity-conflict',
      };
    }
    const exists = local.some((account) => account.meta.name === entry.name);
    return {
      provider: entry.provider,
      name: entry.name,
      envelope: entry.envelope,
      label,
      identity,
      disposition: exists ? 'replace' : 'create',
    };
  });
  return { createdAt: decoded.createdAt, entries };
}

async function readBackupPackage(
  path: string,
  registry: ProviderRegistry,
): Promise<{ createdAt: string; entries: DecodedPackageEntry[] }> {
  let handle;
  try {
    handle = await open(path, 'r');
  } catch {
    throw restoreError('Backup package could not be read.', 'BACKUP_MISSING');
  }

  let encoded: string;
  try {
    let fileStat;
    try {
      fileStat = await handle.stat();
    } catch {
      throw restoreError('Backup package is not valid JSON.', 'BACKUP_FORMAT');
    }
    if (!fileStat.isFile()) {
      throw restoreError('Backup package is not valid JSON.', 'BACKUP_FORMAT');
    }
    if (fileStat.size > MAX_BACKUP_ENCODED_BYTES) {
      throw restoreError('Backup package exceeds the encoded size limit.', 'BACKUP_LIMIT');
    }

    const chunks: Buffer[] = [];
    let bytesRead = 0;
    while (true) {
      const remaining = MAX_BACKUP_ENCODED_BYTES - bytesRead;
      const chunk = Buffer.allocUnsafe(Math.min(64 * 1024, remaining + 1));
      let read;
      try {
        read = await handle.read(chunk, 0, chunk.length, null);
      } catch {
        throw restoreError('Backup package is not valid JSON.', 'BACKUP_FORMAT');
      }
      if (read.bytesRead === 0) {
        break;
      }
      bytesRead += read.bytesRead;
      if (bytesRead > MAX_BACKUP_ENCODED_BYTES) {
        throw restoreError('Backup package exceeds the encoded size limit.', 'BACKUP_LIMIT');
      }
      chunks.push(chunk.subarray(0, read.bytesRead));
    }
    encoded = Buffer.concat(chunks, bytesRead).toString('utf8');
  } finally {
    await handle.close().catch(() => {});
  }

  let parsed: unknown;
  try {
    parsed = JSON.parse(encoded);
  } catch {
    throw restoreError('Backup package is not valid JSON.', 'BACKUP_FORMAT');
  }
  const obj = exactObject(parsed, ['version', 'kind', 'createdAt', 'accounts'], 'backup package');
  if (obj.kind !== ACCOUNT_BACKUP_KIND) {
    throw restoreError('Backup package kind or version is unsupported.', 'BACKUP_FORMAT');
  }
  if (obj.version !== ACCOUNT_BACKUP_VERSION) {
    throw restoreError(
      'Backup package version is newer than this AnyPick version.',
      'BACKUP_VERSION',
    );
  }
  if (!isIsoTimestamp(obj.createdAt)) {
    throw restoreError('Backup package has an invalid creation timestamp.', 'BACKUP_FORMAT');
  }
  if (!Array.isArray(obj.accounts) || obj.accounts.length === 0) {
    throw restoreError('Backup package contains no accounts.', 'BACKUP_FORMAT');
  }
  if (obj.accounts.length > MAX_BACKUP_ACCOUNTS) {
    throw restoreError('Backup package contains too many accounts.', 'BACKUP_LIMIT');
  }

  const seen = new Set<string>();
  const entries: DecodedPackageEntry[] = [];
  let fileCount = 0;
  let decodedBytes = 0;
  for (const rawEntry of obj.accounts) {
    const wrapper = exactObject(rawEntry, ['provider', 'name', 'envelope'], 'backup account entry');
    if (typeof wrapper.provider !== 'string' || !registry.has(wrapper.provider)) {
      throw restoreError('Backup package references an unavailable provider.', 'BACKUP_FORMAT');
    }
    if (typeof wrapper.name !== 'string' || normalizeSafely(wrapper.name) !== wrapper.name) {
      throw restoreError('Backup package contains an invalid account name.', 'BACKUP_FORMAT');
    }
    const key = entryKey({ provider: wrapper.provider, name: wrapper.name });
    if (seen.has(key)) {
      throw restoreError('Backup package contains a duplicate account.', 'BACKUP_FORMAT');
    }
    seen.add(key);

    exactObject(
      wrapper.envelope,
      ['version', 'kind', 'meta', 'proxy', 'files'],
      'account envelope',
    );
    let envelope: DecodedAccountEnvelope;
    try {
      envelope = decodeAccountEnvelope(wrapper.envelope, wrapper.provider);
    } catch (err) {
      const code =
        isAnyPickError(err) && err.code === 'IMPORT_LIMIT' ? 'BACKUP_LIMIT' : 'BACKUP_FORMAT';
      throw restoreError('Backup package contains an invalid account envelope.', code);
    }
    if (envelope.meta.name !== wrapper.name) {
      throw restoreError(
        'Backup account wrapper does not match its envelope metadata.',
        'BACKUP_FORMAT',
      );
    }
    fileCount += Object.keys(envelope.files).length;
    for (const content of Object.values(envelope.files)) {
      decodedBytes += Buffer.from(content, 'base64').byteLength;
    }
    if (fileCount > MAX_BACKUP_FILES || decodedBytes > MAX_BACKUP_DECODED_BYTES) {
      throw restoreError('Backup package exceeds aggregate snapshot limits.', 'BACKUP_LIMIT');
    }
    entries.push({ provider: wrapper.provider, name: wrapper.name, envelope });
  }
  return { createdAt: obj.createdAt, entries };
}

function exactObject(value: unknown, fields: string[], label: string): Record<string, unknown> {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    throw restoreError(`The ${label} is not an object.`, 'BACKUP_FORMAT');
  }
  const keys = Object.keys(value).toSorted();
  if (
    keys.length !== fields.length ||
    keys.some((key, index) => key !== fields.toSorted()[index])
  ) {
    throw restoreError(`The ${label} has unexpected or missing fields.`, 'BACKUP_FORMAT');
  }
  return value as Record<string, unknown>;
}

function validateRefs(
  refs: SavedAccountBackupEntry[],
  code: string,
  allowEmpty = false,
): Set<string> {
  if (refs.length === 0) {
    if (allowEmpty) {
      return new Set();
    }
    throw restoreError('At least one backup account must be selected.', code);
  }
  if (refs.length > MAX_BACKUP_ACCOUNTS) {
    throw restoreError('Too many backup accounts were selected.', code);
  }
  const keys = new Set<string>();
  for (const ref of refs) {
    if (!ref.provider || normalizeSafely(ref.name) !== ref.name) {
      throw restoreError('The restore selection contains an invalid account reference.', code);
    }
    const key = entryKey(ref);
    if (keys.has(key)) {
      throw restoreError('The restore selection contains a duplicate account.', code);
    }
    keys.add(key);
  }
  return keys;
}

function previewEntry(entry: PlannedEntry): AccountRestorePreviewEntry {
  const preview: AccountRestorePreviewEntry = {
    provider: entry.provider,
    name: entry.name,
    label: previewLabel(entry.label, entry.name),
    disposition: entry.disposition,
  };
  if (entry.reason) {
    preview.reason = entry.reason;
  }
  return preview;
}

function normalizeSafely(name: string): string | null {
  try {
    return normalizeAccountName(name);
  } catch {
    return null;
  }
}

function isIsoTimestamp(value: unknown): value is string {
  if (typeof value !== 'string') {
    return false;
  }
  const date = new Date(value);
  return !Number.isNaN(date.valueOf()) && date.toISOString() === value;
}

function previewLabel(value: string | undefined, fallback: string): string {
  const trimmed = value?.trim();
  return trimmed && !/[\p{Cc}\p{Cf}]/u.test(trimmed) ? trimmed.slice(0, 256) : fallback;
}

function normalizedIdentity(identity: string): string {
  return identity.trim().toLowerCase();
}

function identityKey(provider: string, identity: string): string {
  return `${provider}\0${normalizedIdentity(identity)}`;
}

function entryKey(entry: { provider: string; name: string }): string {
  return `${entry.provider}\0${entry.name}`;
}

function restoreError(message: string, code: string): AnyPickError {
  return new AnyPickError(message, { code, mutated: false });
}
