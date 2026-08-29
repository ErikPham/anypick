import type { RuntimeProfile, RuntimeProfileMeta, RuntimeProfileSecrets } from '../types';
import { AnyPickError } from '../utils/errors';
import { displayLabelFromName, normalizeProfileName } from '../utils/slug';
import type { CatalogRegistry } from '../catalog/providers';
import type { ProfileStore } from './profile-store';

/** Models configured once on the profile, applied on `profile use`. */
export interface ProfileModels {
  defaultModel?: string;
  sonnetModel?: string;
  opusModel?: string;
  haikuModel?: string;
  /** Optional alias map (advanced). */
  models?: Record<string, string>;
}

export interface ProfileCreateOpts extends ProfileModels {
  provider: string;
  endpoint?: string;
  apiKey?: string;
  label?: string;
  notes?: string;
  headers?: Record<string, string>;
  clientOverrides?: Record<string, Record<string, unknown>>;
  force?: boolean;
}

export interface ProfileEditOpts extends ProfileModels {
  endpoint?: string;
  apiKey?: string;
  label?: string;
  notes?: string;
  headers?: Record<string, string>;
  clientOverrides?: Record<string, Record<string, unknown>>;
  clearApiKey?: boolean;
  /** Remove alias keys from models map. */
  unsetModels?: string[];
}

export class ProfileService {
  constructor(
    private readonly store: ProfileStore,
    private readonly catalog: CatalogRegistry,
  ) {}

  async list(): Promise<RuntimeProfile[]> {
    return this.store.list();
  }

  async get(name: string): Promise<RuntimeProfile> {
    return this.store.require(normalizeProfileName(name));
  }

  async create(name: string, opts: ProfileCreateOpts): Promise<RuntimeProfile> {
    const profileName = normalizeProfileName(name);
    const existing = await this.store.get(profileName);
    if (existing && !opts.force) {
      throw new AnyPickError(
        `Profile "${profileName}" already exists. Use --force to overwrite.`,
        'PROFILE_EXISTS',
      );
    }

    const provider = this.catalog.get(opts.provider);
    const now = new Date().toISOString();
    const autoLabel = displayLabelFromName(name, profileName);
    const models = opts.models ?? {};
    const headerNames = opts.headers ? Object.keys(opts.headers) : undefined;

    const splitOverrides = splitClientOverrides(opts.clientOverrides);
    const meta: RuntimeProfileMeta = {
      name: profileName,
      provider: provider.id,
      createdAt: existing?.meta.createdAt ?? now,
      updatedAt: now,
      label: opts.label ?? existing?.meta.label ?? autoLabel,
      notes: opts.notes ?? existing?.meta.notes,
      endpoint: opts.endpoint ?? provider.defaultEndpoint,
      headerNames,
      models,
      defaultModel: cleanModel(opts.defaultModel),
      sonnetModel: cleanModel(opts.sonnetModel),
      opusModel: cleanModel(opts.opusModel),
      haikuModel: cleanModel(opts.haikuModel),
      clientOverrides: splitOverrides.meta,
    };

    const secrets: RuntimeProfileSecrets = {
      apiKey: normalizeLooseSecret(opts.apiKey) ?? existing?.secrets.apiKey,
      headers: opts.headers ?? existing?.secrets.headers,
      clientOverrides: mergeSecretOverrides(
        existing?.secrets.clientOverrides,
        splitOverrides.secrets,
      ),
    };

    await this.store.writeMeta(meta);
    await this.store.writeSecrets(profileName, secrets);
    return this.store.require(profileName);
  }

  async edit(name: string, opts: ProfileEditOpts): Promise<RuntimeProfile> {
    const profileName = normalizeProfileName(name);
    const current = await this.store.require(profileName);
    const now = new Date().toISOString();

    const models = { ...current.meta.models, ...opts.models };
    for (const key of opts.unsetModels ?? []) {
      delete models[key];
    }

    let headers = current.secrets.headers ? { ...current.secrets.headers } : undefined;
    if (opts.headers) {
      headers = { ...headers, ...opts.headers };
    }

    const currentOverrides = splitClientOverrides(current.meta.clientOverrides);
    const requestedOverrides = splitClientOverrides(opts.clientOverrides);
    const clientOverrides = {
      ...currentOverrides.meta,
      ...requestedOverrides.meta,
    };

    const meta: RuntimeProfileMeta = {
      ...current.meta,
      updatedAt: now,
      label: opts.label ?? current.meta.label,
      notes: opts.notes ?? current.meta.notes,
      endpoint: opts.endpoint ?? current.meta.endpoint,
      models,
      defaultModel:
        opts.defaultModel !== undefined ? cleanModel(opts.defaultModel) : current.meta.defaultModel,
      sonnetModel:
        opts.sonnetModel !== undefined ? cleanModel(opts.sonnetModel) : current.meta.sonnetModel,
      opusModel: opts.opusModel !== undefined ? cleanModel(opts.opusModel) : current.meta.opusModel,
      haikuModel:
        opts.haikuModel !== undefined ? cleanModel(opts.haikuModel) : current.meta.haikuModel,
      headerNames: headers ? Object.keys(headers) : current.meta.headerNames,
      clientOverrides: Object.keys(clientOverrides).length > 0 ? clientOverrides : undefined,
    };

    let apiKey = current.secrets.apiKey;
    if (opts.clearApiKey) {
      apiKey = undefined;
    }
    if (opts.apiKey !== undefined) {
      apiKey = normalizeLooseSecret(opts.apiKey) ?? '';
    }

    const secrets: RuntimeProfileSecrets = {
      apiKey,
      headers,
      clientOverrides: mergeSecretOverrides(
        mergeSecretOverrides(current.secrets.clientOverrides, currentOverrides.secrets),
        requestedOverrides.secrets,
      ),
    };

    await this.store.writeMeta(meta);
    await this.store.writeSecrets(profileName, secrets);
    return this.store.require(profileName);
  }

  async delete(name: string): Promise<void> {
    await this.store.delete(normalizeProfileName(name));
  }

  async rename(oldName: string, newName: string): Promise<void> {
    await this.store.rename(normalizeProfileName(oldName), normalizeProfileName(newName));
  }

  async duplicate(sourceName: string, newName: string): Promise<RuntimeProfile> {
    const source = await this.store.require(normalizeProfileName(sourceName));
    const dest = normalizeProfileName(newName);
    if (await this.store.get(dest)) {
      throw new AnyPickError(`Profile "${dest}" already exists.`, 'PROFILE_EXISTS');
    }
    return this.create(dest, {
      provider: source.meta.provider,
      endpoint: source.meta.endpoint,
      apiKey: source.secrets.apiKey,
      label: source.meta.label,
      notes: source.meta.notes,
      defaultModel: source.meta.defaultModel,
      sonnetModel: source.meta.sonnetModel,
      opusModel: source.meta.opusModel,
      haikuModel: source.meta.haikuModel,
      models: { ...source.meta.models },
      headers: source.secrets.headers ? { ...source.secrets.headers } : undefined,
      clientOverrides: source.meta.clientOverrides
        ? structuredClone(source.meta.clientOverrides)
        : undefined,
    });
  }

  redact(
    profile: RuntimeProfile,
    reveal = false,
  ): {
    meta: RuntimeProfileMeta;
    secrets: RuntimeProfileSecrets;
  } {
    if (reveal) {
      return { meta: profile.meta, secrets: profile.secrets };
    }
    return {
      meta: redactProfileMeta(profile.meta),
      secrets: {
        apiKey: profile.secrets.apiKey ? maskSecret(profile.secrets.apiKey) : undefined,
        headers: profile.secrets.headers
          ? Object.fromEntries(
              Object.entries(profile.secrets.headers).map(([k, v]) => [k, maskSecret(v)]),
            )
          : undefined,
      },
    };
  }
}

const SECRET_FIELD =
  /(?:api[_-]?key|access[_-]?token|refresh[_-]?token|auth(?:orization)?|secret|password|credential)/i;

function splitClientOverrides(overrides: ProfileCreateOpts['clientOverrides']): {
  meta?: Record<string, Record<string, unknown>>;
  secrets?: Record<string, { apiKey?: string; env?: Record<string, string> }>;
} {
  if (!overrides) {
    return {};
  }
  const meta: Record<string, Record<string, unknown>> = {};
  const secrets: Record<string, { apiKey?: string; env?: Record<string, string> }> = {};
  for (const [client, raw] of Object.entries(overrides)) {
    const next = structuredClone(raw);
    const secret: { apiKey?: string; env?: Record<string, string> } = {};
    if (typeof next.apiKey === 'string') {
      secret.apiKey = next.apiKey;
      delete next.apiKey;
    }
    if (next.env && typeof next.env === 'object' && !Array.isArray(next.env)) {
      const publicEnv: Record<string, unknown> = {};
      const secretEnv: Record<string, string> = {};
      for (const [key, value] of Object.entries(next.env as Record<string, unknown>)) {
        if (SECRET_FIELD.test(key)) {
          if (typeof value !== 'string') {
            throw new AnyPickError(
              `Secret environment override ${key} must be a string.`,
              'PROFILE_SECRET_INVALID',
            );
          }
          secretEnv[key] = value;
        } else {
          publicEnv[key] = value;
        }
      }
      if (Object.keys(secretEnv).length > 0) {
        secret.env = secretEnv;
      }
      if (Object.keys(publicEnv).length > 0) {
        next.env = publicEnv;
      } else {
        delete next.env;
      }
    }
    rejectNestedSecrets(next, `clientOverrides.${client}`);
    if (Object.keys(next).length > 0) {
      meta[client] = next;
    }
    if (secret.apiKey !== undefined || secret.env) {
      secrets[client] = secret;
    }
  }
  return {
    meta: Object.keys(meta).length > 0 ? meta : undefined,
    secrets: Object.keys(secrets).length > 0 ? secrets : undefined,
  };
}

function rejectNestedSecrets(value: unknown, path: string): void {
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    return;
  }
  for (const [key, child] of Object.entries(value)) {
    if (SECRET_FIELD.test(key)) {
      throw new AnyPickError(
        `${path}.${key} is credential material. Put it in apiKey or an environment override instead.`,
        'PROFILE_SECRET_INVALID',
      );
    }
    rejectNestedSecrets(child, `${path}.${key}`);
  }
}

function mergeSecretOverrides(
  base: RuntimeProfileSecrets['clientOverrides'],
  additions: RuntimeProfileSecrets['clientOverrides'],
): RuntimeProfileSecrets['clientOverrides'] {
  const merged: NonNullable<RuntimeProfileSecrets['clientOverrides']> = structuredClone(base ?? {});
  for (const [client, addition] of Object.entries(additions ?? {})) {
    const current = merged[client] ?? {};
    merged[client] = {
      apiKey: addition.apiKey ?? current.apiKey,
      env: { ...current.env, ...addition.env },
    };
  }
  return Object.keys(merged).length > 0 ? merged : undefined;
}

function redactProfileMeta(meta: RuntimeProfileMeta): RuntimeProfileMeta {
  return {
    ...meta,
    clientOverrides: meta.clientOverrides
      ? (redactUnknown(meta.clientOverrides) as Record<string, Record<string, unknown>>)
      : undefined,
  };
}

function redactUnknown(value: unknown, fieldName = ''): unknown {
  if (SECRET_FIELD.test(fieldName)) {
    return '••••••••';
  }
  if (Array.isArray(value)) {
    return value.map((child) => redactUnknown(child));
  }
  if (!value || typeof value !== 'object') {
    return value;
  }
  return Object.fromEntries(
    Object.entries(value).map(([key, child]) => [key, redactUnknown(child, key)]),
  );
}

function cleanModel(value: string | undefined): string | undefined {
  if (value === undefined) {
    return undefined;
  }
  const t = value.trim();
  return t.length ? t : undefined;
}

function normalizeLooseSecret(value: string | undefined): string | undefined {
  if (value === undefined) {
    return undefined;
  }
  return value.replace(/^\r?\n|\r?\n$/g, '');
}

function maskSecret(value: string): string {
  if (value.length === 0) {
    return '(empty)';
  }
  if (value.length <= 8) {
    return '••••••••';
  }
  return `${value.slice(0, 4)}…${value.slice(-4)}`;
}
