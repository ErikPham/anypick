import { join } from 'node:path';
import { homedir } from 'node:os';
import { readFile } from 'node:fs/promises';
import { pathExists } from '../utils/fs';

/** Where the Codex CLI keeps its persisted login. */
export type CodexCredentialStore = 'file' | 'keyring' | 'auto' | 'unknown';

export interface CodexCredentialStoreStatus {
  mode: CodexCredentialStore;
  /** True only when AnyPick can atomically snapshot and restore the live credential. */
  supportsSnapshotSwitch: boolean;
  configPath: string;
}

/**
 * Read Codex's credential-store choice without interpreting the rest of its
 * TOML configuration. `file` is Codex's default when no setting is present.
 *
 * `auto` is deliberately not treated as file. On machines with a usable OS
 * credential store it can resolve to the keyring, where writing auth.json
 * would leave the actual CLI login unchanged.
 */
export async function inspectCodexCredentialStore(
  home = process.env.CODEX_HOME ?? homedir(),
): Promise<CodexCredentialStoreStatus> {
  const configPath = join(home, 'config.toml');
  let mode: CodexCredentialStore = 'file';

  if (await pathExists(configPath)) {
    try {
      const content = await readFile(configPath, 'utf8');
      const configured = readTopLevelString(content, 'cli_auth_credentials_store');
      if (configured) {
        mode = isCredentialStore(configured) ? configured : 'unknown';
      }
    } catch {
      // Existing but unreadable configuration is not proof that auth.json is
      // authoritative. Prefer a recoverable refusal over a false switch.
      mode = 'unknown';
    }
  }

  return {
    mode,
    supportsSnapshotSwitch: mode === 'file',
    configPath,
  };
}

/** Human-safe remediation that never contains credential material. */
export function codexCredentialStoreGuidance(status: CodexCredentialStoreStatus): string {
  const setting = 'cli_auth_credentials_store = "file"';
  if (status.mode === 'auto') {
    return `Codex is configured to use auto credential storage, which may select the OS keyring. Set ${setting} in ${status.configPath}, then sign in again before using account switching.`;
  }
  if (status.mode === 'keyring') {
    return `Codex is configured to use the OS keyring. Set ${setting} in ${status.configPath}, then sign in again before using account switching.`;
  }
  return `Codex credential storage is not a supported file store. Set ${setting} in ${status.configPath}, then sign in again before using account switching.`;
}

function isCredentialStore(value: string): value is Exclude<CodexCredentialStore, 'unknown'> {
  return value === 'file' || value === 'keyring' || value === 'auto';
}

/**
 * Minimal TOML reader for one root scalar. The setting is root-only in Codex's
 * config; accepting an assignment after a table header would accidentally read
 * an unrelated plugin/provider value.
 */
function readTopLevelString(content: string, key: string): string | undefined {
  let inRoot = true;
  const pattern = new RegExp(`^\\s*${escapeRegExp(key)}\\s*=\\s*(.+?)\\s*(?:#.*)?$`);
  for (const line of content.split(/\r?\n/u)) {
    const trimmed = line.trim();
    if (!trimmed || trimmed.startsWith('#')) {
      continue;
    }
    if (/^\[\[?.+\]\]?$/u.test(trimmed)) {
      inRoot = false;
      continue;
    }
    if (!inRoot) {
      continue;
    }
    const match = line.match(pattern);
    if (!match?.[1]) {
      continue;
    }
    const value = match[1].trim();
    if (value.startsWith('"') && value.endsWith('"') && value.length >= 2) {
      try {
        return JSON.parse(value) as string;
      } catch {
        return undefined;
      }
    }
    if (value.startsWith("'") && value.endsWith("'") && value.length >= 2) {
      return value.slice(1, -1);
    }
    return value;
  }
  return undefined;
}

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/gu, '\\$&');
}
