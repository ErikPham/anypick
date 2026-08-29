import type {
  CompatibleCredentialDefinition,
  CompatibleEndpointProfile,
  CompatibleProviderDefinition,
} from '../../types';
import { AnyPickError } from '../../utils/errors';

export const COMPATIBLE_CREDENTIAL_FILE = 'compatible-credential.json';

export interface StoredCompatibleCredential {
  version: 1;
  kind: string;
  profile: string;
  secret: string;
}

const ID = /^[a-z0-9][a-z0-9-]{1,63}$/;

function invalid(message: string): never {
  throw new AnyPickError(message, 'INVALID_COMPATIBLE_PROVIDER');
}

function validateBaseUrl(raw: string, label: string): void {
  let url: URL;
  try {
    url = new URL(raw);
  } catch {
    invalid(`${label} is not a valid URL: ${raw}`);
  }
  if (url.protocol !== 'https:') {
    invalid(`${label} must use HTTPS.`);
  }
  if (url.username || url.password || url.search || url.hash) {
    invalid(`${label} must not contain credentials, query parameters, or a fragment.`);
  }
  if (url.hostname === 'localhost' || url.hostname === '127.0.0.1' || url.hostname === '::1') {
    invalid(`${label} must not target loopback.`);
  }
}

export function validateCompatibleProviderDefinition(
  definition: CompatibleProviderDefinition,
): CompatibleProviderDefinition {
  if (!ID.test(definition.id)) {
    invalid(`Invalid compatible provider id "${definition.id}".`);
  }
  if (!definition.name.trim() || !definition.description.trim()) {
    invalid(`Compatible provider "${definition.id}" needs a name and description.`);
  }
  if (
    definition.defaultProxyPort != null &&
    (!Number.isInteger(definition.defaultProxyPort) ||
      definition.defaultProxyPort < 1 ||
      definition.defaultProxyPort > 65535)
  ) {
    invalid(`Compatible provider "${definition.id}" has an invalid proxy port.`);
  }
  const models = definition.models.map((model) => model.trim()).filter(Boolean);
  if (!models.length) {
    invalid(`Compatible provider "${definition.id}" needs a fallback model.`);
  }
  if (!definition.credentials.length) {
    invalid(`Compatible provider "${definition.id}" needs at least one credential kind.`);
  }
  const kinds = new Set<string>();
  for (const credential of definition.credentials) {
    if (!ID.test(credential.kind) || kinds.has(credential.kind)) {
      invalid(
        `Compatible provider "${definition.id}" has invalid/duplicate kind "${credential.kind}".`,
      );
    }
    kinds.add(credential.kind);
    if (!credential.label.trim() || !credential.profiles.length) {
      invalid(`Credential "${credential.kind}" needs a label and endpoint profile.`);
    }
    const profiles = new Set<string>();
    for (const profile of credential.profiles) {
      if (!ID.test(profile.id) || profiles.has(profile.id)) {
        invalid(`Credential "${credential.kind}" has invalid/duplicate profile "${profile.id}".`);
      }
      profiles.add(profile.id);
      validateBaseUrl(profile.openaiBase, `${definition.id}/${profile.id} OpenAI base URL`);
      if (profile.anthropicBase) {
        validateBaseUrl(profile.anthropicBase, `${definition.id}/${profile.id} Anthropic base URL`);
      }
      for (const [field, scheme] of [
        ['OpenAI', profile.openaiAuth],
        ['Anthropic', profile.anthropicAuth],
      ] as const) {
        if (scheme == null || ['bearer', 'x-api-key', 'api-key'].includes(scheme)) {
          continue;
        }
        invalid(
          `Credential "${credential.kind}" profile "${profile.id}" has an invalid ${field} auth scheme.`,
        );
      }
      if (profile.nativeResponses != null && typeof profile.nativeResponses !== 'boolean') {
        invalid(
          `Credential "${credential.kind}" profile "${profile.id}" has an invalid nativeResponses flag.`,
        );
      }
    }
  }
  for (const pattern of definition.quotaErrorPatterns ?? []) {
    try {
      void new RegExp(pattern, 'iu');
    } catch {
      invalid(`Compatible provider "${definition.id}" has invalid quota pattern "${pattern}".`);
    }
  }
  return { ...definition, models: [...new Set(models)] };
}

export function credentialDefinition(
  definition: CompatibleProviderDefinition,
  kind: string,
): CompatibleCredentialDefinition | undefined {
  return definition.credentials.find((credential) => credential.kind === kind);
}

export function endpointProfile(
  definition: CompatibleProviderDefinition,
  kind: string,
  profileId: string,
): CompatibleEndpointProfile | undefined {
  return credentialDefinition(definition, kind)?.profiles.find(
    (profile) => profile.id === profileId,
  );
}

export function trimBaseUrl(url: string): string {
  return url.replace(/\/+$/u, '');
}
