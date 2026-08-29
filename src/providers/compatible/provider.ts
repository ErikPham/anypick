import { createHash } from 'node:crypto';
import { join } from 'node:path';
import type {
  Account,
  CompatibleProviderDefinition,
  CredentialInput,
  CredentialInputField,
  LiveAuthStatus,
  Provider,
  ProxyContext,
  ProxyHandle,
  ProxyHubBackendContext,
  ProxyHubBackendHandle,
  ProxyStatus,
  SnapshotMeta,
  SourceAdapter,
} from '../../types';
import { accountRef } from '../../core/refs';
import { AnyPickError } from '../../utils/errors';
import { readJsonFile, writeJsonFile } from '../../utils/fs';
import { closeProxyHubBackend } from '../proxy-hub-backend';
import {
  COMPATIBLE_CREDENTIAL_FILE,
  credentialDefinition,
  endpointProfile,
  type StoredCompatibleCredential,
  validateCompatibleProviderDefinition,
} from './definition';
import {
  readCompatibleProxyLogs,
  startCompatibleProxy,
  statusCompatibleProxy,
  stopCompatibleProxy,
} from './lifecycle';
import { listenCompatibleProxy } from './server';

const DEFAULT_PORT = 4140;

function proxySource(providerId: string, name: string): SourceAdapter {
  return {
    sourceRef: accountRef(providerId, name),
    capabilities: {
      sourceKind: 'account',
      provider: providerId,
      nativeClients: [],
      protocols: ['openai', 'anthropic'],
      canRefresh: false,
      supportsModelDiscovery: true,
      requiresNativeAuthWrite: false,
    },
    transportFor(clientId) {
      return clientId === 'claude' || clientId === 'codex'
        ? 'managed_builtin_proxy'
        : 'unsupported';
    },
  };
}

export class CompatibleProvider implements Provider {
  readonly id: string;
  readonly name: string;
  readonly shortName?: string;
  readonly description: string;
  readonly defaultProxyPort: number;
  readonly proxyCompatibility = 'OpenAI + Anthropic API';
  readonly credentialInputs: readonly string[];
  readonly definition: CompatibleProviderDefinition;

  constructor(rawDefinition: CompatibleProviderDefinition) {
    const definition = validateCompatibleProviderDefinition(rawDefinition);
    this.definition = definition;
    this.id = definition.id;
    this.name = definition.name;
    this.shortName = definition.shortName;
    this.description = definition.description;
    this.defaultProxyPort = definition.defaultProxyPort ?? DEFAULT_PORT;
    this.credentialInputs = definition.credentials.map((credential) => credential.kind);
  }

  roleDefaults(): Record<string, string> {
    const model = this.definition.models[0] ?? '';
    return { default: model, sonnet: model, opus: model, haiku: model };
  }

  roleFriendlyModels(): readonly string[] {
    return this.definition.models;
  }

  credentialInputLabel(kind: string): string {
    return credentialDefinition(this.definition, kind)?.label ?? kind.replaceAll('-', ' ');
  }

  credentialInputFields(kind: string): readonly CredentialInputField[] {
    const credential = credentialDefinition(this.definition, kind);
    if (!credential || credential.profiles.length <= 1) {
      return [];
    }
    return [
      {
        name: 'profile',
        label: 'Endpoint profile',
        choices: credential.profiles.map((profile) => profile.id),
        default: credential.profiles[0]?.id,
      },
    ];
  }

  async detectLive(): Promise<LiveAuthStatus> {
    return { present: false };
  }

  async backup(): Promise<SnapshotMeta> {
    throw new AnyPickError(
      `${this.name} has no native login to save. Add an API or subscription credential instead.`,
      'NO_LIVE_AUTH',
    );
  }

  async backupInput(input: CredentialInput, destDir: string): Promise<SnapshotMeta> {
    const credential = credentialDefinition(this.definition, input.kind);
    if (!credential) {
      throw new AnyPickError(
        `${this.name} does not accept credential kind "${input.kind}".`,
        'UNSUPPORTED_CREDENTIAL_INPUT',
      );
    }
    const secret = input.secret.trim();
    if (!secret) {
      throw new AnyPickError(`${credential.label} is empty.`, 'INVALID_CREDENTIAL');
    }
    const profile = input.options?.profile ?? credential.profiles[0]?.id ?? '';
    if (!endpointProfile(this.definition, input.kind, profile)) {
      throw new AnyPickError(
        `Unknown ${this.name} endpoint profile "${profile}" for ${credential.label}.`,
        'INVALID_CREDENTIAL',
      );
    }
    const stored: StoredCompatibleCredential = {
      version: 1,
      kind: input.kind,
      profile,
      secret,
    };
    await writeJsonFile(join(destDir, COMPATIBLE_CREDENTIAL_FILE), stored, 0o600);
    const hash = createHash('sha256').update(secret).digest('hex').slice(0, 12);
    return {
      identity: `${input.kind}:${hash}`,
      label: `${credential.label} · ${profile}`,
      credentialKind: 'proxy-only',
      credentialClass: credential.credentialClass ?? 'provider_api_key',
    };
  }

  /** Proxy-only credentials never replace a vendor CLI's native login. */
  async restore(): Promise<void> {}

  sourceAdapter(account: Account): SourceAdapter {
    return proxySource(this.id, account.meta.name);
  }

  poolSourceAdapter(): SourceAdapter {
    return {
      ...proxySource(this.id, '__pool__'),
      sourceRef: { kind: 'account-pool', provider: this.id },
    };
  }

  async startProxy(ctx: ProxyContext): Promise<ProxyHandle> {
    return startCompatibleProxy(
      ctx,
      this.definition,
      this.proxyCompatibility,
      this.defaultProxyPort,
    );
  }

  async stopProxy(ctx: ProxyContext): Promise<void> {
    await stopCompatibleProxy(ctx);
  }

  async proxyStatus(ctx: ProxyContext): Promise<ProxyStatus> {
    return statusCompatibleProxy(ctx, this.proxyCompatibility, this.defaultProxyPort);
  }

  async readProxyLogs(ctx: ProxyContext, lines?: number): Promise<string> {
    return readCompatibleProxyLogs(ctx, lines);
  }

  async createProxyHubBackend(ctx: ProxyHubBackendContext): Promise<ProxyHubBackendHandle> {
    const { server, endpoint } = await listenCompatibleProxy({
      host: '127.0.0.1',
      port: 0,
      definition: this.definition,
      authDirs: ctx.accounts.map((account) => account.snapshotDir),
      accountNames: ctx.accounts.map((account) => account.name),
      token: ctx.token,
      log: ctx.log,
    });
    return { endpoint, close: () => closeProxyHubBackend(server) };
  }
}

export async function readStoredCompatibleCredential(
  snapshotDir: string,
): Promise<StoredCompatibleCredential> {
  const value = await readJsonFile<StoredCompatibleCredential>(
    join(snapshotDir, COMPATIBLE_CREDENTIAL_FILE),
  );
  if (
    value.version !== 1 ||
    typeof value.kind !== 'string' ||
    typeof value.profile !== 'string' ||
    typeof value.secret !== 'string' ||
    !value.secret
  ) {
    throw new AnyPickError(
      `Invalid ${COMPATIBLE_CREDENTIAL_FILE} in ${snapshotDir}.`,
      'INVALID_CREDENTIAL',
    );
  }
  return value;
}
