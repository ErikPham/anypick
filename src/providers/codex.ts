import { join } from 'node:path';
import { homedir } from 'node:os';
import { rm } from 'node:fs/promises';
import type {
  Account,
  AccountMeta,
  LiveAuthStatus,
  LiveUsage,
  ProviderLoginContext,
  ProxyContext,
  ProxyHandle,
  ProxyHubBackendContext,
  ProxyHubBackendHandle,
  ProxyStatus,
  Provider,
  SourceAdapter,
} from '../types';
import { parseCodexUsage } from './codex-usage';
import { OPENAI_MODELS } from '../catalog/providers';
import { codexAccountAdapter, poolAdapterFor } from '../sources/account-adapters';
import {
  backupRequiredFile,
  expandHome,
  pathExists,
  readJsonFile,
  restoreRequiredFile,
  writeJsonFile,
} from '../utils/fs';
import { refreshCodexAuth, type CodexAuthFile } from './codex-refresh';
import { fingerprintLiveAuth, fingerprintSnapshot } from './auth-fingerprint';
import { hasCodexAuth, codexAccountId, extractCodexIdentity } from './codex-identity';
import {
  codexProxyStatus,
  readCodexProxyLogs,
  startCodexProxy,
  stopCodexProxy,
} from './codex-proxy-lifecycle';
import { listenCodexProxy } from './codex-proxy/server';
import { closeProxyHubBackend } from './proxy-hub-backend';
import { AnyPickError } from '../utils/errors';
import {
  loginMethodAvailability,
  runBrowserLoginCommand,
  type BrowserLoginCommand,
  type BrowserLoginRunner,
} from './browser-login';
import {
  codexCredentialStoreGuidance,
  inspectCodexCredentialStore,
  type CodexCredentialStoreStatus,
} from './codex-credential-store';

const DEFAULT_PROXY_PORT = 4140;
const CODEX_LOGIN_METHOD = 'chatgpt-subscription';

/**
 * OpenAI Codex CLI + Desktop
 *
 * CLI live auth: ~/.codex/auth.json
 * Desktop app may keep a different in-memory ChatGPT account (multi-account),
 * but AnyPick can only back up and restore ~/.codex/auth.json. Therefore
 * detectLive() deliberately reports that file, keeping detection, backup,
 * restore, and snapshot matching on one authority.
 */
export class CodexProvider implements Provider {
  readonly id = 'codex';
  readonly name = 'OpenAI Codex';
  readonly shortName = 'Codex';
  readonly description = 'Manages ~/.codex/auth.json';
  readonly defaultProxyPort = DEFAULT_PROXY_PORT;
  readonly proxyCompatibility = 'OpenAI Responses API';

  roleDefaults(): Record<string, string> {
    return { default: 'gpt-5.6-sol' };
  }

  suggestModels(): Record<string, string> {
    return { ...OPENAI_MODELS };
  }

  roleFriendlyModels(): readonly string[] {
    return ['gpt-5.6-sol', 'gpt-5.6-terra', 'gpt-5.6-luna', 'gpt-5.3-codex'];
  }

  staticFallbackModels(): readonly string[] {
    return ['gpt-5.6-sol', 'gpt-5.6-terra', 'gpt-5.3-codex'];
  }

  constructor(
    private readonly home = homedir(),
    private readonly fetchFn: typeof fetch = fetch,
    private readonly loginRunner: BrowserLoginRunner = runBrowserLoginCommand,
  ) {}

  private get loginCommand(): BrowserLoginCommand {
    return {
      names: [process.env.CODEX_BINARY, 'codex'].filter((value): value is string => Boolean(value)),
      extraPaths: [
        join(this.home, '.local', 'bin', 'codex'),
        '/opt/homebrew/bin/codex',
        '/usr/local/bin/codex',
      ],
      args: ['login'],
    };
  }

  async loginMethods() {
    return [
      await loginMethodAvailability(
        {
          id: CODEX_LOGIN_METHOD,
          label: 'ChatGPT subscription',
          description: 'OpenAI Codex browser sign-in',
          provenance: 'official',
          preferred: true,
          source: 'codex',
        },
        this.loginCommand,
      ),
    ];
  }

  async login(ctx: ProviderLoginContext): Promise<void> {
    if (ctx.methodId !== CODEX_LOGIN_METHOD || (ctx.source && ctx.source !== 'codex')) {
      throw new AnyPickError('Unknown Codex sign-in method.', 'PROVIDER_LOGIN_METHOD_UNKNOWN');
    }
    await this.assertFileCredentialStore();
    await this.loginRunner(this.loginCommand, { home: this.home, signal: ctx.signal });
  }

  sourceAdapter(account: Account): SourceAdapter {
    return codexAccountAdapter(account);
  }

  poolSourceAdapter(): SourceAdapter {
    // Codex OAuth upstream speaks Responses only; do not advertise it to
    // Claude until an Anthropic translation boundary exists.
    return poolAdapterFor(this.id, this, { acceptedClients: ['codex'] });
  }

  async createProxyHubBackend(ctx: ProxyHubBackendContext): Promise<ProxyHubBackendHandle> {
    const [primary, ...rest] = ctx.accounts;
    if (!primary) {
      throw new Error('Codex Hub backend requires at least one account');
    }
    const options = primary.proxy.options ?? {};
    const { server, endpoint } = await listenCodexProxy({
      host: '127.0.0.1',
      port: 0,
      authPath: join(primary.snapshotDir, 'auth.json'),
      authPaths: rest.map((account) => join(account.snapshotDir, 'auth.json')),
      authAccountNames: ctx.accounts.map((account) => account.name),
      upstream: typeof options.upstream === 'string' ? options.upstream : undefined,
      token: ctx.token,
      // Hub reads canonical snapshots. OAuth refresh stays in memory so the
      // provider backend never mutates them outside the coordinator.
      persistRefreshedAuth: false,
      log: ctx.log,
    });
    return { endpoint, close: () => closeProxyHubBackend(server) };
  }

  private get authPath(): string {
    return process.env.CODEX_AUTH_PATH ?? join(this.codexHome, 'auth.json');
  }

  private get codexHome(): string {
    return process.env.CODEX_HOME ?? join(this.home, '.codex');
  }

  private async assertFileCredentialStore(): Promise<CodexCredentialStoreStatus> {
    const store = await inspectCodexCredentialStore(this.codexHome);
    if (!store.supportsSnapshotSwitch) {
      throw new AnyPickError(codexCredentialStoreGuidance(store), {
        code: 'CODEX_CREDENTIAL_STORE_UNSUPPORTED',
        suggestions: [
          `Set cli_auth_credentials_store = "file" in ${store.configPath}.`,
          'Run codex login again so auth.json becomes the authoritative credential.',
        ],
      });
    }
    return store;
  }

  async detectLive(): Promise<LiveAuthStatus> {
    const store = await inspectCodexCredentialStore(this.codexHome);
    if (!store.supportsSnapshotSwitch) {
      return {
        present: false,
        details: `Credential store: ${store.mode}. ${codexCredentialStoreGuidance(store)}`,
      };
    }
    return this.detectFromAuthFile();
  }

  /**
   * Read the Codex/ChatGPT rate-limit snapshot for auth.json only. When
   * Codex Desktop reports another account as active, auth.json may be stale;
   * in that case return nothing rather than risk showing another account's
   * quota.
   */
  async liveUsage(): Promise<LiveUsage | null> {
    const file = await this.detectFromAuthFile();
    if (!file.present || !file.accountId) {
      return null;
    }
    const desktop = await this.detectFromDesktopApp();
    if (desktop?.accountId && desktop.accountId !== file.accountId) {
      return null;
    }

    try {
      const auth = await readJsonFile<CodexAuthFile>(this.authPath);
      const accessToken = auth.tokens?.access_token;
      if (!accessToken) {
        return null;
      }
      const response = await this.fetchFn('https://chatgpt.com/backend-api/wham/usage', {
        headers: {
          authorization: `Bearer ${accessToken}`,
          'chatgpt-account-id': file.accountId,
        },
        signal: AbortSignal.timeout(5_000),
      });
      if (!response.ok) {
        return null;
      }
      return parseCodexUsage(await response.json());
    } catch {
      // Usage is optional; account detection/switching must remain available.
      return null;
    }
  }

  /** CLI / file-based session (~/.codex/auth.json). */
  private async detectFromAuthFile(): Promise<LiveAuthStatus & { accountId?: string }> {
    const path = this.authPath;
    if (!(await pathExists(path))) {
      return { present: false };
    }
    try {
      const data = await readJsonFile<Record<string, unknown>>(path);
      if (!hasCodexAuth(data)) {
        return { present: false };
      }
      const identity = extractCodexIdentity(data);
      const accountId = codexAccountId(data);
      const mode = typeof data.auth_mode === 'string' ? data.auth_mode : undefined;
      return {
        present: true,
        identity,
        accountId,
        details: [mode, accountId ? `acct:${accountId.slice(0, 8)}` : null]
          .filter(Boolean)
          .join(' '),
      };
    } catch {
      return { present: true, details: 'auth.json present (unreadable)' };
    }
  }

  /**
   * Codex Desktop writes the signed-in ChatGPT account into Sentry scope
   * (and rate-limit UI state). That is often ahead of ~/.codex/auth.json
   * after an in-app account switch.
   */
  private async detectFromDesktopApp(): Promise<{
    accountId: string;
    identity?: string;
  } | null> {
    const candidates = [
      join(this.home, 'Library', 'Application Support', 'Codex', 'sentry', 'scope_v3.json'),
      // Linux / other
      join(this.home, '.config', 'Codex', 'sentry', 'scope_v3.json'),
    ];

    for (const path of candidates) {
      if (!(await pathExists(path))) {
        continue;
      }
      try {
        const raw = await readJsonFile<Record<string, unknown>>(path);
        const scope = (raw.scope ?? raw) as Record<string, unknown>;
        const user = scope.user as Record<string, unknown> | undefined;
        if (!user || typeof user !== 'object') {
          continue;
        }
        const accountId = typeof user.account_id === 'string' ? user.account_id.trim() : '';
        if (!accountId) {
          continue;
        }
        // Optional: only trust ChatGPT auth method
        const method = typeof user.authMethod === 'string' ? user.authMethod : undefined;
        if (method && method !== 'chatgpt') {
          continue;
        }
        return {
          accountId,
          identity: typeof user.email === 'string' ? user.email : undefined,
        };
      } catch {
        // try next path
      }
    }

    // Fallback: rate-limit dismissal map keys are ChatGPT account ids the app knows.
    // Prefer the most recently dismissed / only key when sentry is missing.
    try {
      const statePath = join(this.home, '.codex', '.codex-global-state.json');
      if (await pathExists(statePath)) {
        const state = await readJsonFile<Record<string, unknown>>(statePath);
        const atom = state['electron-persisted-atom-state'] as Record<string, unknown> | undefined;
        const map = atom?.['rate-limit-reset-home-announcement-dismissal-by-account-id'] as
          | Record<string, { dismissedAtMs?: number }>
          | undefined;
        if (map && typeof map === 'object') {
          const entries = Object.entries(map);
          if (entries.length === 1 && entries[0]?.[0]) {
            return { accountId: entries[0][0] };
          }
          // most recently dismissed
          let best: { id: string; ts: number } | null = null;
          for (const [id, v] of entries) {
            const ts = typeof v?.dismissedAtMs === 'number' ? v.dismissedAtMs : 0;
            if (!best || ts > best.ts) {
              best = { id, ts };
            }
          }
          if (best?.id) {
            return { accountId: best.id };
          }
        }
      }
    } catch {
      // ignore
    }

    return null;
  }

  async backup(
    destDir: string,
  ): Promise<Partial<Pick<AccountMeta, 'identity' | 'label' | 'notes'>>> {
    await this.assertFileCredentialStore();
    const dest = join(destDir, 'auth.json');
    await backupRequiredFile(this.authPath, dest, 'Codex auth.json');
    try {
      const data = await readJsonFile<Record<string, unknown>>(dest);
      return { identity: extractCodexIdentity(data) };
    } catch {
      return {};
    }
  }

  async restore(srcDir: string): Promise<void> {
    await this.assertFileCredentialStore();
    await restoreRequiredFile(join(srcDir, 'auth.json'), expandHome(this.authPath), 'auth.json');
  }

  async preflightRestore(_srcDir: string): Promise<void> {
    await this.assertFileCredentialStore();
  }

  async describeSnapshot(
    srcDir: string,
  ): Promise<Partial<Pick<AccountMeta, 'identity' | 'label' | 'notes'>>> {
    const path = join(srcDir, 'auth.json');
    if (!(await pathExists(path))) {
      return {};
    }
    try {
      const data = await readJsonFile<Record<string, unknown>>(path);
      return { identity: extractCodexIdentity(data) };
    } catch {
      return {};
    }
  }

  /**
   * Delete local auth.json only — does NOT call OpenAI logout/revoke.
   * Saved anypick snapshots remain valid for restore.
   */
  async clearLive(): Promise<void> {
    const path = expandHome(this.authPath);
    for (const p of [path, `${path}.lock`]) {
      try {
        await rm(p, { force: true });
      } catch {
        // ignore
      }
    }
  }

  /**
   * Refresh ChatGPT OAuth tokens inside a snapshot dir (auth.json).
   */
  async refreshAuth(
    authDir: string,
  ): Promise<Partial<Pick<AccountMeta, 'identity' | 'label' | 'notes'>>> {
    const path = join(authDir, 'auth.json');
    if (!(await pathExists(path))) {
      throw new Error(`No auth.json in ${authDir}`);
    }
    const current = await readJsonFile<CodexAuthFile>(path);
    const { auth, identity } = await refreshCodexAuth(current);
    await writeJsonFile(path, auth, 0o600);
    return { identity };
  }

  /**
   * Compare this snapshot's session material against the live Codex login.
   * Prefers refresh_token (unique per session) over account_id so two
   * snapshots of the same ChatGPT account are not both marked live.
   */
  async snapshotMatchesLive(snapshotDir: string): Promise<boolean> {
    if (!(await inspectCodexCredentialStore(this.codexHome)).supportsSnapshotSwitch) {
      return false;
    }
    const liveFp = await fingerprintLiveAuth('codex', this.home);
    if (!liveFp) {
      return false;
    }
    const snap = await fingerprintSnapshot('codex', snapshotDir);
    return snap != null && snap === liveFp;
  }

  async startProxy(ctx: ProxyContext): Promise<ProxyHandle> {
    return startCodexProxy(ctx, {
      defaultPort: DEFAULT_PROXY_PORT,
      compatibility: this.proxyCompatibility,
    });
  }

  async stopProxy(ctx: ProxyContext): Promise<void> {
    await stopCodexProxy(ctx);
  }

  async proxyStatus(ctx: ProxyContext): Promise<ProxyStatus> {
    return codexProxyStatus(ctx, this.proxyCompatibility, DEFAULT_PROXY_PORT);
  }

  async readProxyLogs(ctx: ProxyContext, lines = 50): Promise<string> {
    return readCodexProxyLogs(ctx, lines);
  }
}

export const codexProvider = new CodexProvider();
