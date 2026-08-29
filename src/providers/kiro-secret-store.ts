/**
 * kiro-cli's secret store.
 *
 * Since kiro-cli 2.x the login is no longer a file under ~/.aws/sso/cache. It
 * lives in the OS keychain, mirrored into an `auth_kv` table in kiro-cli's own
 * SQLite database, and kiro-cli reads the keychain *first* — so switching a Kiro
 * account has to write both tiers. Restoring only the database would be
 * silently ignored while a stale keychain item is still readable, which is the
 * worst possible outcome: anypick reports a switch that did not happen.
 *
 * Every access mirrors what kiro-cli itself does — `/usr/bin/security` and
 * `INSERT OR REPLACE INTO auth_kv` — so an item anypick writes carries the same
 * keychain ACL and kiro-cli reads it back without an authorization prompt.
 *
 * The secret never crosses `argv`: `security(1)` is driven through its `-i`
 * stdin mode, and the value is passed as hex (`-X`) because `-i` unquoting
 * mangles backslashes, which JSON escapes contain.
 */

import { execFile } from 'node:child_process';
import { homedir } from 'node:os';
import { join } from 'node:path';
import { openForeignDatabase } from '../core/db';
import { AnyPickError } from '../utils/errors';

/** Login kinds kiro-cli stores, each under its own keychain service name. */
export const KIRO_SECRET_KEYS = [
  'kirocli:social:token',
  'kirocli:odic:token',
  'kirocli:odic:device-registration',
  'kirocli:external-idp:token',
] as const;

/** Keyed by the names in `KIRO_SECRET_KEYS`; absent keys are simply not stored. */
export type KiroSecrets = Record<string, string>;

const SECURITY_BIN = '/usr/bin/security';
const SECURITY_TIMEOUT_MS = 5_000;

/** Set by the test suite so no test can touch the developer's real keychain. */
function keychainEnabled(): boolean {
  return process.platform === 'darwin' && process.env.ANYPICK_KIRO_NO_KEYCHAIN !== '1';
}

/** kiro-cli's data directory, following the same platform rules it does. */
export function kiroSecretDbPath(home = homedir()): string {
  const override = process.env.ANYPICK_KIRO_SECRET_DB;
  if (override) {
    return override;
  }
  if (process.platform === 'darwin') {
    return join(home, 'Library', 'Application Support', 'kiro-cli', 'data.sqlite3');
  }
  if (process.platform === 'win32') {
    const base = process.env.LOCALAPPDATA ?? join(home, 'AppData', 'Local');
    return join(base, 'kiro-cli', 'data.sqlite3');
  }
  const xdg = process.env.XDG_DATA_HOME ?? join(home, '.local', 'share');
  return join(xdg, 'kiro-cli', 'data.sqlite3');
}

/** Every secret kiro-cli currently holds. Empty when it is not logged in. */
export async function readKiroSecrets(home = homedir()): Promise<KiroSecrets> {
  const secrets: KiroSecrets = {};

  if (keychainEnabled()) {
    for (const key of KIRO_SECRET_KEYS) {
      const value = await keychainRead(key);
      if (value) {
        secrets[key] = value;
      }
    }
  }

  // Not an else: the keychain can hold a subset, and kiro-cli falls back to the
  // database per key rather than per store.
  const rows = readAuthKv(kiroSecretDbPath(home));
  for (const key of KIRO_SECRET_KEYS) {
    const value = rows.get(key);
    if (value && !secrets[key]) {
      secrets[key] = value;
    }
  }

  return secrets;
}

/**
 * Replace the stored login in both stores. Kiro reads Keychain first, so a
 * Keychain denial is a failed switch, never a successful database-only write.
 */
export async function writeKiroSecrets(secrets: KiroSecrets, home = homedir()): Promise<void> {
  if (!keychainEnabled()) {
    writeAuthKv(kiroSecretDbPath(home), secrets);
    return;
  }

  const before = await readKeychainSecrets();
  try {
    await writeKeychainSecrets(secrets);
    const effective = await readKeychainSecrets();
    if (!sameSecrets(effective, secrets)) {
      throw new AnyPickError(
        'Kiro Keychain did not retain the selected credentials.',
        'KIRO_KEYCHAIN_WRITE_FAILED',
      );
    }
    writeAuthKv(kiroSecretDbPath(home), secrets);
  } catch (err) {
    // Best-effort compensation keeps an unsuccessful activation from silently
    // changing what the upstream CLI reads. Preserve the original error.
    await writeKeychainSecrets(before).catch(() => {});
    throw err;
  }
}

/** Forget the local login. Does not call Kiro/AWS logout. */
export async function clearKiroSecrets(home = homedir()): Promise<void> {
  await writeKiroSecrets({}, home);
}

/**
 * A human label for the stored login.
 *
 * The token carries no email — the only per-account signal is the profile id at
 * the tail of `profile_arn`, so it is paired with the identity provider to give
 * something a person can tell two Google logins apart by.
 */
export function kiroSecretIdentity(secrets: KiroSecrets): string | undefined {
  for (const key of KIRO_SECRET_KEYS) {
    const raw = secrets[key];
    if (!raw) {
      continue;
    }
    let parsed: unknown;
    try {
      parsed = JSON.parse(raw);
    } catch {
      continue;
    }
    if (!parsed || typeof parsed !== 'object') {
      continue;
    }
    const record = parsed as Record<string, unknown>;
    const arn = record.profile_arn ?? record.profileArn;
    if (typeof arn !== 'string') {
      continue;
    }
    const profile = arn.split('/').pop();
    if (!profile) {
      continue;
    }
    const provider = record.provider;
    return typeof provider === 'string' && provider ? `${provider}:${profile}` : profile;
  }
  return undefined;
}

function readAuthKv(dbPath: string): Map<string, string> {
  const rows = new Map<string, string>();
  let db;
  try {
    db = openForeignDatabase(dbPath, true);
  } catch {
    // No kiro-cli database at all, or an older schema: nothing stored.
    return rows;
  }
  try {
    for (const row of db.prepare('SELECT key, value FROM auth_kv').all() as {
      key?: unknown;
      value?: unknown;
    }[]) {
      if (typeof row.key === 'string' && typeof row.value === 'string') {
        rows.set(row.key, row.value);
      }
    }
  } catch {
    // Table absent on an older kiro-cli schema.
  } finally {
    db.close();
  }
  return rows;
}

function writeAuthKv(dbPath: string, secrets: KiroSecrets): void {
  let db;
  try {
    db = openForeignDatabase(dbPath);
  } catch {
    if (Object.values(secrets).every((value) => !value)) {
      // A file-only Kiro login has no auth_kv tier to clear. This is safe only
      // for an empty update; importing/restoring actual secrets must never be a
      // successful no-op on a machine without the authoritative store.
      return;
    }
    throw new AnyPickError(
      'Kiro credential database could not be opened; refusing a partial credential switch.',
      'KIRO_SECRET_STORE_UNAVAILABLE',
    );
  }
  try {
    db.exec('CREATE TABLE IF NOT EXISTS auth_kv (key TEXT PRIMARY KEY, value TEXT)');
    db.transaction(() => {
      const upsert = db.prepare('INSERT OR REPLACE INTO auth_kv (key, value) VALUES (?, ?)');
      const remove = db.prepare('DELETE FROM auth_kv WHERE key = ?');
      for (const key of KIRO_SECRET_KEYS) {
        const value = secrets[key];
        if (value) {
          upsert.run(key, value);
        } else {
          remove.run(key);
        }
      }
    });
  } finally {
    db.close();
  }
}

function keychainRead(service: string): Promise<string | undefined> {
  return new Promise((resolve, reject) => {
    execFile(
      SECURITY_BIN,
      ['find-generic-password', '-s', service, '-w'],
      { timeout: SECURITY_TIMEOUT_MS },
      (err, stdout, stderr) => {
        if (!err) {
          resolve(stdout.trim() || undefined);
          return;
        }
        // Missing is the only expected read failure. A locked/denied keychain
        // is not equivalent to an empty credential and must surface.
        if (/could not be found|item not found/i.test(stderr)) {
          resolve(undefined);
          return;
        }
        reject(
          new AnyPickError(
            'macOS Keychain could not read Kiro credentials.',
            'KIRO_KEYCHAIN_READ_FAILED',
          ),
        );
      },
    );
  });
}

async function readKeychainSecrets(): Promise<KiroSecrets> {
  const secrets: KiroSecrets = {};
  for (const key of KIRO_SECRET_KEYS) {
    const value = await keychainRead(key);
    if (value) {
      secrets[key] = value;
    }
  }
  return secrets;
}

async function writeKeychainSecrets(secrets: KiroSecrets): Promise<void> {
  for (const key of KIRO_SECRET_KEYS) {
    const value = secrets[key];
    if (value) {
      await keychainWrite(key, value);
    } else {
      await keychainDelete(key);
    }
  }
}

function sameSecrets(actual: KiroSecrets, expected: KiroSecrets): boolean {
  return KIRO_SECRET_KEYS.every((key) => actual[key] === expected[key]);
}

function keychainWrite(service: string, value: string): Promise<void> {
  const hex = Buffer.from(value, 'utf8').toString('hex');
  // -a "" matches the NULL account kiro-cli's own items carry, so an update
  // replaces its item instead of creating a second one beside it.
  return securityStdin(`add-generic-password -U -s "${service}" -a "" -X ${hex}\n`);
}

async function keychainDelete(service: string): Promise<void> {
  try {
    await securityStdin(`delete-generic-password -s "${service}"\n`);
  } catch (err) {
    // Empty slots are expected when restoring a partial login. Everything else
    // (locked keychain, ACL denial, timeout) must abort the switch.
    if (err instanceof AnyPickError && err.code === 'KIRO_KEYCHAIN_ITEM_MISSING') {
      return;
    }
    throw err;
  }
}

/**
 * Drive `security(1)` through its batch mode so the secret is never an argument.
 * A locked or denied Keychain makes the switch fail closed; callers then undo
 * their activation checkpoint rather than report a credential that Kiro cannot
 * actually use.
 */
function securityStdin(command: string): Promise<void> {
  return new Promise((resolve, reject) => {
    const child = execFile(
      SECURITY_BIN,
      ['-i'],
      { timeout: SECURITY_TIMEOUT_MS },
      (err, _stdout, stderr) => {
        if (!err) {
          resolve();
          return;
        }
        reject(
          new AnyPickError(
            'macOS Keychain rejected the Kiro credential update.',
            /could not be found|item not found/i.test(stderr)
              ? 'KIRO_KEYCHAIN_ITEM_MISSING'
              : 'KIRO_KEYCHAIN_WRITE_FAILED',
          ),
        );
      },
    );
    child.stdin?.end(command);
  });
}
