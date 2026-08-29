import { mkdtemp, rm } from 'node:fs/promises';
import { join } from 'node:path';
import type { Account, AccountMeta, AccountProxyConfig } from '../types';
import { DEFAULT_PROXY_CONFIG } from '../types';
import { AnyPickError } from '../utils/errors';
import { ensureDir, removePath } from '../utils/fs';
import { decodeWithFallback, decoders } from './codec';
import type { AnyPickDatabase } from './db';
import {
  accountDir,
  accountProxyRuntimeDir,
  accountSnapshotDir,
  getAnyPickRoot,
  proxyLogPath,
  proxyPidPath,
  snapshotStagingDir,
} from './paths';
import { ingestSnapshotDir, materializeSnapshot, rowToAccount } from './store-snapshot';

export interface AccountBatchWrite {
  meta: AccountMeta;
  proxy: AccountProxyConfig;
  snapshotDir: string;
}

export interface ProxyRuntimeState {
  accountName: string;
  endpoint: string;
  compatibility?: string;
  pid?: number;
  /** Opaque child identity used to prove a reused endpoint is still ours. */
  instanceId?: string;
  logPath?: string;
  startedAt: string;
  /**
   * Per-instance high-entropy secret (PROXY-01) required on every credentialed
   * route. Persisted owner-only in proxy_state; never surfaced in status/doctor.
   */
  token?: string;
}

/**
 * SQLite-backed account store.
 *
 * Structured data lives in ~/.anypick/anypick.db.
 * Snapshot bytes are BLOBs; they are materialized to a cache dir when
 * providers need a real filesystem path for backup/restore.
 * Proxy pid/log runtime stays on disk (process-local).
 */
export class AccountStore {
  readonly root: string;
  readonly db: AnyPickDatabase;
  private readonly preparedSnapshots = new Map<string, Set<string>>();

  constructor(root: string, db: AnyPickDatabase) {
    this.root = getAnyPickRoot(root);
    this.db = db;
  }

  async ensureProvider(_providerId: string): Promise<void> {
    // No-op: SQLite has no per-provider directories to create.
  }

  async listAccounts(providerId: string): Promise<Account[]> {
    const rows = this.db
      .prepare(
        `SELECT name, meta_json, proxy_json FROM accounts
         WHERE provider = ?
           AND EXISTS (
             SELECT 1 FROM account_snapshot_files
             WHERE account_snapshot_files.provider = accounts.provider
               AND account_snapshot_files.account = accounts.name
           )
         ORDER BY name`,
      )
      .all(providerId) as Array<{ name: string; meta_json: string; proxy_json: string }>;

    return rows.map((row) => rowToAccount(this.root, providerId, row));
  }

  async getAccount(providerId: string, name: string): Promise<Account | null> {
    const row = this.db
      .prepare(
        `SELECT name, meta_json, proxy_json FROM accounts
         WHERE provider = ? AND name = ?
           AND EXISTS (
             SELECT 1 FROM account_snapshot_files
             WHERE account_snapshot_files.provider = accounts.provider
               AND account_snapshot_files.account = accounts.name
           )`,
      )
      .get(providerId, name) as { name: string; meta_json: string; proxy_json: string } | undefined;
    if (!row) {
      return null;
    }
    const account = rowToAccount(this.root, providerId, row);
    // Ensure snapshot files are on disk for provider.restore / export
    materializeSnapshot(this.root, this.db, providerId, name);
    return account;
  }

  /**
   * Metadata-only update. Snapshot BLOBs stay as they are — used to correct
   * stale display fields without re-ingesting credential files.
   */
  async patchAccountMeta(
    providerId: string,
    name: string,
    patch: Partial<Pick<AccountMeta, 'identity' | 'label' | 'notes' | 'credentialClass'>>,
  ): Promise<AccountMeta> {
    const account = await this.getAccount(providerId, name);
    if (!account) {
      throw new AnyPickError(
        `No account "${name}" for provider "${providerId}".`,
        'ACCOUNT_NOT_FOUND',
      );
    }
    const now = new Date().toISOString();
    const next: AccountMeta = { ...account.meta, updatedAt: now };
    if ('identity' in patch) {
      if (patch.identity) {
        next.identity = patch.identity;
      } else {
        delete next.identity;
      }
    }
    if ('label' in patch && patch.label !== undefined) {
      next.label = patch.label;
    }
    if ('notes' in patch) {
      if (patch.notes) {
        next.notes = patch.notes;
      } else {
        delete next.notes;
      }
    }
    if (patch.credentialClass) {
      next.credentialClass = patch.credentialClass;
    }
    this.db
      .prepare(`UPDATE accounts SET meta_json = ?, updated_at = ? WHERE provider = ? AND name = ?`)
      .run(JSON.stringify(next), now, providerId, name);
    return next;
  }

  async requireAccount(providerId: string, name: string): Promise<Account> {
    const account = await this.getAccount(providerId, name);
    if (!account) {
      throw new AnyPickError(
        `No account "${name}" for provider "${providerId}".`,
        'ACCOUNT_NOT_FOUND',
      );
    }
    return account;
  }

  /**
   * Prepare an isolated snapshot directory for writing a new/updated account.
   * Caller fills the snapshot (provider.backup / import), THEN calls
   * `writeMeta`, which atomically replaces the
   * snapshot's DB rows + metadata.
   *
   * NOTE: prepareSnapshot must NOT delete the existing snapshot's DB rows —
   * doing so before backup succeeds would destroy the previous snapshot on a
   * fault (DATA-01). The previous rows are replaced only inside
   * `writeMeta` transaction.
   */
  async prepareSnapshot(
    providerId: string,
    name: string,
  ): Promise<{ snapshotDir: string; accountDir: string }> {
    // Do not reuse accountSnapshotDir here. getAccount() is intentionally
    // allowed to materialize that cache from SQLite, so sharing it with a
    // backup/import staging area could make a successful save commit stale or
    // mixed credentials (ADR 0004).
    const stagingRoot = snapshotStagingDir(this.root);
    await ensureDir(stagingRoot);
    const snap = await mkdtemp(join(stagingRoot, 'snapshot-'));
    const key = `${providerId}\u0000${name}`;
    const pending = this.preparedSnapshots.get(key) ?? new Set<string>();
    pending.add(snap);
    this.preparedSnapshots.set(key, pending);
    return {
      snapshotDir: snap,
      accountDir: accountDir(this.root, providerId, name),
    };
  }

  /**
   * Atomically replace an account's metadata + snapshot files in one
   * transaction. Callers MUST fill `snapshotDir` (via provider.backup or import)
   * before calling. Any throw leaves the previous rows + metadata exactly as
   * they were (DATA-01): the old snapshot files are deleted and re-ingested
   * together, never partially.
   */
  async writeMeta(meta: AccountMeta, snapshotDir?: string): Promise<void> {
    const now = meta.updatedAt || new Date().toISOString();
    // The default supports metadata-only updates and legacy callers that first
    // materialize the committed cache. New backup/import flows must pass their
    // operation-private staging directory.
    const key = `${meta.provider}\u0000${meta.name}`;
    const pending = this.preparedSnapshots.get(key);
    if (!snapshotDir && pending && pending.size > 1) {
      throw new AnyPickError(
        'Multiple prepared snapshots exist for this account; pass the staging path explicitly.',
        'SNAPSHOT_AMBIGUOUS',
      );
    }
    const snap =
      snapshotDir ??
      pending?.values().next().value ??
      accountSnapshotDir(this.root, meta.provider, meta.name);

    this.db.transaction(() => {
      const existing = this.db
        .prepare(`SELECT proxy_json, created_at FROM accounts WHERE provider = ? AND name = ?`)
        .get(meta.provider, meta.name) as { proxy_json: string; created_at: string } | undefined;

      const proxyJson = existing?.proxy_json ?? JSON.stringify(DEFAULT_PROXY_CONFIG);
      const createdAt = existing?.created_at ?? meta.createdAt ?? now;

      this.db
        .prepare(
          `INSERT INTO accounts (provider, name, meta_json, proxy_json, created_at, updated_at)
           VALUES (?, ?, ?, ?, ?, ?)
           ON CONFLICT(provider, name) DO UPDATE SET
             meta_json = excluded.meta_json,
             updated_at = excluded.updated_at`,
        )
        .run(
          meta.provider,
          meta.name,
          JSON.stringify({ ...meta, updatedAt: now }),
          proxyJson,
          createdAt,
          now,
        );

      // Delete + re-ingest snapshot files atomically so a partial write cannot
      // strand old rows.
      this.db
        .prepare(`DELETE FROM account_snapshot_files WHERE provider = ? AND account = ?`)
        .run(meta.provider, meta.name);
      ingestSnapshotDir(this.db, meta.provider, meta.name, snap);
    });

    // Snapshot paths are a cache only. Failing to refresh one must not turn a
    // durable SQLite commit into a failed save; the next read will rebuild it.
    try {
      materializeSnapshot(this.root, this.db, meta.provider, meta.name);
    } catch {
      // Best effort cache refresh.
    }
    if (pending?.delete(snap) && pending.size === 0) {
      this.preparedSnapshots.delete(key);
    }
  }

  async discardPreparedSnapshot(snapshotDir: string): Promise<void> {
    const root = snapshotStagingDir(this.root);
    // A provider receives this path, so only delete a directory we created
    // below our dedicated staging root.
    if (snapshotDir === root || !snapshotDir.startsWith(`${root}/`)) {
      throw new AnyPickError('Refusing to remove a snapshot outside the staging area.', 'FS_PATH');
    }
    for (const [key, pending] of this.preparedSnapshots) {
      pending.delete(snapshotDir);
      if (pending.size === 0) {
        this.preparedSnapshots.delete(key);
      }
    }
    await rm(snapshotDir, { recursive: true, force: true });
  }

  /** Commit multiple restored accounts in one SQLite transaction. */
  async writeAccountsBatch(entries: AccountBatchWrite[]): Promise<void> {
    this.db.transaction(() => {
      for (const entry of entries) {
        const { meta } = entry;
        const now = meta.updatedAt || new Date().toISOString();
        const existing = this.db
          .prepare(`SELECT created_at FROM accounts WHERE provider = ? AND name = ?`)
          .get(meta.provider, meta.name) as { created_at: string } | undefined;
        const createdAt = existing?.created_at ?? meta.createdAt ?? now;
        const storedMeta = { ...meta, createdAt, updatedAt: now };

        this.db
          .prepare(
            `INSERT INTO accounts (provider, name, meta_json, proxy_json, created_at, updated_at)
             VALUES (?, ?, ?, ?, ?, ?)
             ON CONFLICT(provider, name) DO UPDATE SET
               meta_json = excluded.meta_json,
               proxy_json = excluded.proxy_json,
               updated_at = excluded.updated_at`,
          )
          .run(
            meta.provider,
            meta.name,
            JSON.stringify(storedMeta),
            JSON.stringify(entry.proxy),
            createdAt,
            now,
          );
        ingestSnapshotDir(this.db, meta.provider, meta.name, entry.snapshotDir);
      }
    });

    // Snapshot directories are a cache; the transaction above is authoritative.
    for (const { meta } of entries) {
      try {
        materializeSnapshot(this.root, this.db, meta.provider, meta.name);
      } catch {
        // A later getAccount rematerializes the cache from committed BLOB rows.
      }
    }
  }

  async getProxyConfig(providerId: string, name: string): Promise<AccountProxyConfig> {
    const row = this.db
      .prepare(`SELECT proxy_json FROM accounts WHERE provider = ? AND name = ?`)
      .get(providerId, name) as { proxy_json: string } | undefined;
    if (!row) {
      return { ...DEFAULT_PROXY_CONFIG };
    }
    return decodeWithFallback(
      row.proxy_json,
      decoders.accountProxyConfig,
      {
        ...DEFAULT_PROXY_CONFIG,
      },
      `${providerId}/${name}`,
    );
  }

  async setProxyConfig(
    providerId: string,
    name: string,
    config: AccountProxyConfig,
  ): Promise<void> {
    await this.requireAccount(providerId, name);
    this.db
      .prepare(`UPDATE accounts SET proxy_json = ?, updated_at = ? WHERE provider = ? AND name = ?`)
      .run(JSON.stringify(config), new Date().toISOString(), providerId, name);
  }

  runtimeDir(providerId: string, name: string): string {
    return accountProxyRuntimeDir(this.root, providerId, name);
  }

  pidPath(providerId: string, name: string): string {
    return proxyPidPath(this.root, providerId, name);
  }

  logPath(providerId: string, name: string): string {
    return proxyLogPath(this.root, providerId, name);
  }

  async writeProxyState(providerId: string, name: string, state: ProxyRuntimeState): Promise<void> {
    this.db
      .prepare(
        `INSERT INTO proxy_state (provider, account, state_json)
         VALUES (?, ?, ?)
         ON CONFLICT(provider, account) DO UPDATE SET state_json = excluded.state_json`,
      )
      .run(providerId, name, JSON.stringify(state));
  }

  async readProxyState(providerId: string, name: string): Promise<ProxyRuntimeState | null> {
    const row = this.db
      .prepare(`SELECT state_json FROM proxy_state WHERE provider = ? AND account = ?`)
      .get(providerId, name) as { state_json: string } | undefined;
    if (!row) {
      return null;
    }
    return decodeWithFallback(
      row.state_json,
      decoders.proxyRuntimeState,
      null as ProxyRuntimeState | null,
      `${providerId}/${name}`,
    );
  }

  async clearProxyState(providerId: string, name: string): Promise<void> {
    this.db
      .prepare(`DELETE FROM proxy_state WHERE provider = ? AND account = ?`)
      .run(providerId, name);
  }

  async deleteAccount(providerId: string, name: string): Promise<void> {
    const deleted = this.db.transaction(() => {
      const result = this.db
        .prepare(`DELETE FROM accounts WHERE provider = ? AND name = ?`)
        .run(providerId, name);
      if (result.changes === 0) {
        return false;
      }
      this.db
        .prepare(`DELETE FROM proxy_state WHERE provider = ? AND account = ?`)
        .run(providerId, name);
      // The active pointer participates in the same durable commit as account
      // deletion. Clearing it after filesystem cleanup could strand a pointer
      // to a deleted account after a crash or an rm permission failure.
      const active = this.db
        .prepare(`SELECT account_name FROM provider_active WHERE provider = ?`)
        .get(providerId) as { account_name: string } | undefined;
      if (active?.account_name === name) {
        this.db.prepare(`DELETE FROM provider_active WHERE provider = ?`).run(providerId);
      }
      return true;
    });
    if (!deleted) {
      throw new AnyPickError(
        `No account "${name}" for provider "${providerId}".`,
        'ACCOUNT_NOT_FOUND',
      );
    }

    // Clear materialize cache (best-effort; DB state already consistent)
    const snap = accountSnapshotDir(this.root, providerId, name);
    await removePath(snap).catch(() => {});
    await removePath(accountDir(this.root, providerId, name)).catch(() => {});
  }

  async getActive(providerId: string): Promise<string | null> {
    const row = this.db
      .prepare(`SELECT account_name FROM provider_active WHERE provider = ?`)
      .get(providerId) as { account_name: string } | undefined;
    return row?.account_name ?? null;
  }

  async setActive(providerId: string, name: string): Promise<void> {
    this.db
      .prepare(
        `INSERT INTO provider_active (provider, account_name)
         VALUES (?, ?)
         ON CONFLICT(provider) DO UPDATE SET account_name = excluded.account_name`,
      )
      .run(providerId, name);
  }

  async clearActive(providerId: string): Promise<void> {
    this.db.prepare(`DELETE FROM provider_active WHERE provider = ?`).run(providerId);
  }
}
