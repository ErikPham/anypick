import { homedir } from 'node:os';
import { dirname, join } from 'node:path';
import { readFile } from 'node:fs/promises';
import type {
  ApplyContext,
  ClientAdapter,
  ClientInspectResult,
  ClientLiveState,
  ClientState,
  IsolatablePath,
  IsolatedClientRuntime,
  ResolvedClientPlan,
} from '../types';
import { AnyPickError } from '../utils/errors';
import {
  ensureDir,
  pathExists,
  readJsonFile,
  removePath,
  writeJsonFile,
  writeTextFile,
} from '../utils/fs';
import { parseJsonc } from '../utils/jsonc';
import { resolveFromContext } from './resolve';
import {
  createTempRuntimeRoot,
  makeIsolatedRuntime,
  materializeIsolatablePaths,
  syntheticProxyProfile,
} from './isolation';
import { DEFAULT_MODEL_ROLE } from './model-roles';

const MANAGED_PROVIDER_ID = 'anypick-managed';
const RESTORE_FILE = 'restore.json';

interface OpenCodeRestoreState {
  version: 1;
  configPath: string;
  providerPresent: boolean;
  providerValue?: unknown;
  modelPresent: boolean;
  modelValue?: unknown;
  smallModelPresent: boolean;
  smallModelValue?: unknown;
}

function configDir(home: string): string {
  const xdg = home === homedir() ? process.env.XDG_CONFIG_HOME : undefined;
  return join(xdg ?? join(home, '.config'), 'opencode');
}

function candidateConfigPaths(home: string): [string, string] {
  const dir = configDir(home);
  return [join(dir, 'opencode.json'), join(dir, 'opencode.jsonc')];
}

async function effectiveConfigPath(home: string): Promise<string> {
  const [json, jsonc] = candidateConfigPaths(home);
  if (await pathExists(json)) {
    return json;
  }
  if (await pathExists(jsonc)) {
    return jsonc;
  }
  return json;
}

async function readConfig(path: string): Promise<Record<string, unknown>> {
  if (!(await pathExists(path))) {
    return {};
  }
  try {
    const parsed = parseJsonc(await readFile(path, 'utf8'));
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
      throw new Error('root value is not an object');
    }
    return parsed as Record<string, unknown>;
  } catch (error) {
    throw new AnyPickError(
      `Refusing to overwrite unreadable OpenCode config at ${path}: ${error instanceof Error ? error.message : String(error)}`,
      'CLIENT_CONFIG_INVALID',
    );
  }
}

function providersIn(doc: Record<string, unknown>): Record<string, unknown> {
  const value = doc.provider;
  if (value == null) {
    return {};
  }
  if (typeof value !== 'object' || Array.isArray(value)) {
    throw new AnyPickError('OpenCode config.provider must be an object.', 'CLIENT_CONFIG_INVALID');
  }
  return { ...(value as Record<string, unknown>) };
}

function endpointForOpenCode(endpoint: string): string {
  return endpoint.replace(/\/$/u, '').replace(/\/v1$/u, '') + '/v1';
}

export function createOpenCodeClient(home = process.env.HOME ?? homedir()): ClientAdapter {
  async function applyToHome(ctx: ApplyContext, targetHome: string) {
    const runtime = resolveFromContext(ctx);
    const configPath = await effectiveConfigPath(targetHome);
    const secretPath = join(configDir(targetHome), '.anypick-api-key');
    const restorePath = join(ctx.anypickRoot, 'clients', 'opencode', RESTORE_FILE);
    const modelIds = [
      ...new Set([runtime.defaultModel, ...Object.values(runtime.models)].filter(Boolean)),
    ] as string[];
    if (!runtime.endpoint || !runtime.defaultModel || modelIds.length === 0) {
      throw new AnyPickError(
        'OpenCode routing needs an endpoint and a selected default model.',
        'CLIENT_CONFIG_INVALID',
      );
    }

    if (ctx.dryRun) {
      return { managedPaths: [configPath, secretPath, restorePath], managedEnvKeys: [] };
    }

    const doc = await readConfig(configPath);
    const providers = providersIn(doc);
    if (!ctx.isolatedHome && !(await pathExists(restorePath))) {
      const restore: OpenCodeRestoreState = {
        version: 1,
        configPath,
        providerPresent: Object.hasOwn(providers, MANAGED_PROVIDER_ID),
        providerValue: providers[MANAGED_PROVIDER_ID],
        modelPresent: Object.hasOwn(doc, 'model'),
        modelValue: doc.model,
        smallModelPresent: Object.hasOwn(doc, 'small_model'),
        smallModelValue: doc.small_model,
      };
      await ensureDir(dirname(restorePath));
      await writeJsonFile(restorePath, restore, 0o600);
    }

    await ensureDir(configDir(targetHome));
    await writeTextFile(secretPath, runtime.apiKey || 'anypick-proxy', 0o600);
    providers[MANAGED_PROVIDER_ID] = {
      npm: '@ai-sdk/openai-compatible',
      name: 'AnyPick',
      options: {
        baseURL: endpointForOpenCode(runtime.endpoint),
        apiKey: `{file:${secretPath}}`,
        ...(Object.keys(runtime.headers).length ? { headers: runtime.headers } : {}),
      },
      models: Object.fromEntries(modelIds.map((id) => [id, { name: id }])),
    };
    doc.provider = providers;
    doc.model = `${MANAGED_PROVIDER_ID}/${runtime.defaultModel}`;
    await writeJsonFile(configPath, doc, 0o600);
    return {
      managedPaths: [configPath, secretPath, ...(ctx.isolatedHome ? [] : [restorePath])],
      managedEnvKeys: [],
    };
  }

  const adapter: ClientAdapter = {
    id: 'opencode',
    name: 'OpenCode',
    shortName: 'OpenCode',
    binaryName: 'opencode',
    binaryEnvVar: 'OPENCODE_BINARY',
    description: 'OpenCode CLI (multi-provider runtime)',
    supportedApiStyles: ['openai', 'custom'],
    nativeInstallations: [{ sourceId: 'opencode', executables: ['opencode'] }],
    routingSurfacePolicy: 'all-compatible',
    capabilities: {
      id: 'opencode',
      acceptedProtocols: ['openai'],
      supportsEnvironmentOverlay: false,
      supportsIsolatedHome: true,
      supportsPersistentConfig: true,
      protocolPreference: 'openai',
    },
    modelRoles: () => DEFAULT_MODEL_ROLE,

    async validate(ctx) {
      const runtime = resolveFromContext(ctx);
      if (!runtime.endpoint || !runtime.defaultModel) {
        throw new AnyPickError(
          'OpenCode routing needs an endpoint and a selected default model.',
          'CLIENT_CONFIG_INVALID',
        );
      }
    },

    async apply(ctx) {
      return applyToHome(ctx, ctx.isolatedHome ?? home);
    },

    async listIsolatablePaths(liveState: ClientLiveState): Promise<readonly IsolatablePath[]> {
      const configPath = await effectiveConfigPath(liveState.home);
      return [
        {
          sourcePath: configPath,
          destinationPath: join(
            '.config',
            'opencode',
            configPath.endsWith('.jsonc') ? 'opencode.jsonc' : 'opencode.json',
          ),
          kind: 'file',
          required: false,
        },
      ];
    },

    async createIsolatedRuntime(
      plan: ResolvedClientPlan,
      paths: readonly IsolatablePath[],
    ): Promise<IsolatedClientRuntime> {
      const runtimeRoot = await createTempRuntimeRoot('anypick-opencode-');
      const isoHome = join(runtimeRoot, 'home');
      await ensureDir(isoHome);
      await materializeIsolatablePaths(isoHome, paths);
      const endpoint = plan.transport.endpoint ?? '';
      const profile =
        plan.profile ??
        syntheticProxyProfile({
          name: plan.source.display,
          endpoint,
          apiKey: plan.transport.managedProxy?.token ?? 'anypick-proxy',
          defaultModel: plan.model.mode === 'explicit' ? plan.model.id : undefined,
        });
      const ctx: ApplyContext = {
        profile,
        clientId: 'opencode',
        dryRun: plan.dryRun,
        verbose: plan.verbose,
        proxyEndpoint: endpoint || undefined,
        anypickRoot: join(isoHome, '.anypick'),
        isolatedHome: isoHome,
      };
      await adapter.validate(ctx);
      if (!plan.dryRun) {
        await applyToHome(ctx, isoHome);
      }
      return makeIsolatedRuntime(runtimeRoot, {
        HOME: isoHome,
        XDG_CONFIG_HOME: join(isoHome, '.config'),
      });
    },

    async applyPersistent(plan: ResolvedClientPlan) {
      const endpoint = plan.transport.endpoint ?? '';
      const profile =
        plan.profile ??
        syntheticProxyProfile({
          name: plan.source.display,
          endpoint,
          apiKey: plan.transport.managedProxy?.token ?? 'anypick-proxy',
          defaultModel: plan.model.mode === 'explicit' ? plan.model.id : undefined,
        });
      const ctx: ApplyContext = {
        profile,
        clientId: 'opencode',
        dryRun: plan.dryRun,
        verbose: plan.verbose,
        proxyEndpoint: endpoint || undefined,
        anypickRoot: plan.anypickRoot,
      };
      await adapter.validate(ctx);
      return applyToHome(ctx, home);
    },

    async reset(state: ClientState): Promise<void> {
      const restorePath = state.managedPaths.find((path) =>
        path.endsWith(`/opencode/${RESTORE_FILE}`),
      );
      const configPath = await effectiveConfigPath(home);
      const doc = await readConfig(configPath);
      const providers = providersIn(doc);
      delete providers[MANAGED_PROVIDER_ID];
      if (restorePath && (await pathExists(restorePath))) {
        const restore = await readJsonFile<OpenCodeRestoreState>(restorePath);
        if (restore.providerPresent) {
          providers[MANAGED_PROVIDER_ID] = restore.providerValue;
        }
        if (restore.modelPresent) {
          doc.model = restore.modelValue;
        } else {
          delete doc.model;
        }
        if (restore.smallModelPresent) {
          doc.small_model = restore.smallModelValue;
        } else {
          delete doc.small_model;
        }
        await removePath(restorePath);
      } else if (typeof doc.model === 'string' && doc.model.startsWith(`${MANAGED_PROVIDER_ID}/`)) {
        delete doc.model;
      }
      if (Object.keys(providers).length) {
        doc.provider = providers;
      } else {
        delete doc.provider;
      }
      await writeJsonFile(configPath, doc, 0o600);
      for (const path of state.managedPaths) {
        if (path.endsWith('.anypick-api-key')) {
          await removePath(path);
        }
      }
    },

    async inspect(): Promise<ClientInspectResult> {
      const configPath = await effectiveConfigPath(home);
      if (!(await pathExists(configPath))) {
        return { present: false, configPaths: [configPath] };
      }
      try {
        const doc = await readConfig(configPath);
        const managed = Object.hasOwn(providersIn(doc), MANAGED_PROVIDER_ID);
        return {
          present: true,
          configPaths: [configPath],
          summary: managed ? 'AnyPick provider configured' : 'config present (no AnyPick provider)',
        };
      } catch {
        return { present: true, configPaths: [configPath], issues: ['OpenCode config unreadable'] };
      }
    },
  };
  return adapter;
}

export const openCodeClient = createOpenCodeClient();
