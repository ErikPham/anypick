import { chmod, link, mkdtemp, rename, rm, writeFile } from 'node:fs/promises';
import { dirname, join } from 'node:path';
import { AnyPickError } from '../utils/errors';
import { normalizeAccountName } from '../utils/slug';
import { buildAccountEnvelope, type AccountEnvelopeV1 } from './account-transfer';
import type { AccountStore } from './store';

export const ACCOUNT_BACKUP_VERSION = 1 as const;
export const ACCOUNT_BACKUP_KIND = 'anypick-backup' as const;
export const MAX_BACKUP_ACCOUNTS = 256;
export const MAX_BACKUP_FILES = 4096;
export const MAX_BACKUP_DECODED_BYTES = 128 * 1024 * 1024;
export const MAX_BACKUP_ENCODED_BYTES = 192 * 1024 * 1024;

export interface SavedAccountBackupEntry {
  provider: string;
  name: string;
}

export interface AccountBackupPackageV1 {
  version: 1;
  kind: 'anypick-backup';
  createdAt: string;
  accounts: Array<{
    provider: string;
    name: string;
    envelope: AccountEnvelopeV1;
  }>;
}

export interface SavedAccountBackupResult {
  file: string;
  accountCount: number;
}

export interface SavedAccountBackupOptions {
  overwrite?: boolean;
}

export interface SavedAccountBackupDeps {
  buildEnvelope?: typeof buildAccountEnvelope;
  now?: () => Date;
}

export async function exportSelectedAccountBackup(
  store: AccountStore,
  accounts: SavedAccountBackupEntry[],
  destination: string,
  opts: SavedAccountBackupOptions = {},
  deps: SavedAccountBackupDeps = {},
): Promise<SavedAccountBackupResult> {
  const selected = validateBackupSelection(accounts);
  const buildEnvelope = deps.buildEnvelope ?? buildAccountEnvelope;
  const packageAccounts: AccountBackupPackageV1['accounts'] = [];
  let fileCount = 0;
  let decodedBytes = 0;

  for (const account of selected) {
    const envelope = await buildEnvelope(store, account.provider, account.name);
    fileCount += Object.keys(envelope.files).length;
    for (const content of Object.values(envelope.files)) {
      decodedBytes += Buffer.from(content, 'base64').byteLength;
    }
    if (fileCount > MAX_BACKUP_FILES || decodedBytes > MAX_BACKUP_DECODED_BYTES) {
      throw backupError('Selected accounts exceed the backup package limits.', 'BACKUP_LIMIT');
    }
    packageAccounts.push({ ...account, envelope });
  }

  const payload: AccountBackupPackageV1 = {
    version: ACCOUNT_BACKUP_VERSION,
    kind: ACCOUNT_BACKUP_KIND,
    createdAt: (deps.now?.() ?? new Date()).toISOString(),
    accounts: packageAccounts,
  };
  const encoded = `${JSON.stringify(payload, null, 2)}\n`;
  if (Buffer.byteLength(encoded) > MAX_BACKUP_ENCODED_BYTES) {
    throw backupError('Backup package exceeds the encoded size limit.', 'BACKUP_LIMIT');
  }

  const stage = await mkdtemp(join(dirname(destination), '.anypick-backup-'));
  const stagedFile = join(stage, 'package.anypick-backup');
  try {
    await writeFile(stagedFile, encoded, { mode: 0o600 });
    await chmod(stagedFile, 0o600);
    if (opts.overwrite) {
      await rename(stagedFile, destination);
    } else {
      try {
        await link(stagedFile, destination);
      } catch (err) {
        if (isNodeError(err) && err.code === 'EEXIST') {
          throw backupError('The backup destination already exists.', 'BACKUP_DESTINATION_EXISTS');
        }
        throw err;
      }
    }
  } finally {
    await rm(stage, { recursive: true, force: true }).catch(() => {});
  }

  return { file: destination, accountCount: selected.length };
}

export function validateBackupSelection(
  accounts: SavedAccountBackupEntry[],
): SavedAccountBackupEntry[] {
  if (accounts.length === 0) {
    throw backupError('No saved accounts were selected for backup.', 'BACKUP_EMPTY');
  }
  if (accounts.length > MAX_BACKUP_ACCOUNTS) {
    throw backupError(
      `A backup can contain at most ${MAX_BACKUP_ACCOUNTS} accounts.`,
      'BACKUP_LIMIT',
    );
  }

  const seen = new Set<string>();
  const selected = accounts.map(({ provider, name }) => {
    if (!provider) {
      throw backupError('A selected backup account has no provider.', 'BACKUP_SELECTION_INVALID');
    }
    const normalizedName = normalizeAccountName(name);
    const key = `${provider}\0${normalizedName}`;
    if (seen.has(key)) {
      throw backupError('The backup selection contains a duplicate account.', 'BACKUP_DUPLICATE');
    }
    seen.add(key);
    return { provider, name: normalizedName };
  });
  return selected.toSorted(
    (a, b) => a.provider.localeCompare(b.provider) || a.name.localeCompare(b.name),
  );
}

function isNodeError(err: unknown): err is NodeJS.ErrnoException {
  return err instanceof Error && 'code' in err;
}

function backupError(message: string, code: string): AnyPickError {
  return new AnyPickError(message, { code, mutated: false });
}
