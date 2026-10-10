import { snapshotFixture } from '../test/fixture.ts';
import { encodeBase64Utf8 } from '../lib/wire.ts';

/**
 * Encode the UI test fixture as the single snapshot frame the desktop bridge
 * consumes. Building it in-process keeps visual tools independent of the
 * Vite dev-service plugin and the developer's real AnyPick data.
 */
export function emitFixtureLine(rich = false): string {
  const base = snapshotFixture();
  const snapshot = rich
    ? {
        ...base,
        actions: [
          ...base.actions,
          route('gemini', 'Gemini'),
          route('opencode', 'OpenCode'),
          route('kiro', 'Kiro'),
        ],
        accountProviders: [
          ...base.accountProviders,
          {
            id: 'gemini',
            providerId: 'gemini',
            label: 'Gemini',
            detail: 'Google Gemini',
            installed: true,
            livePresent: false,
            loginMethods: [
              { id: 'subscription', label: 'Subscription', provenance: 'official', preferred: true, available: true },
            ],
          },
          {
            id: 'opencode',
            providerId: 'opencode',
            label: 'OpenCode',
            detail: 'OpenCode Zen',
            installed: true,
            livePresent: false,
            loginMethods: [
              { id: 'subscription', label: 'Subscription', provenance: 'official', preferred: true, available: true },
            ],
          },
          {
            id: 'kiro',
            providerId: 'kiro',
            label: 'Kiro',
            detail: 'Amazon Kiro',
            installed: true,
            livePresent: false,
            loginMethods: [
              { id: 'subscription', label: 'Subscription', provenance: 'official', preferred: true, available: true },
            ],
          },
        ],
        gatewayProviders: [
          {
            id: 'openrouter',
            label: 'OpenRouter',
            detail: 'Hosted models',
            requiresApiKey: true,
            providesEndpoint: true,
            defaultEndpoint: 'https://openrouter.ai/api/v1',
            firstPartyClientIds: ['claude', 'codex'],
            kind: 'hosted',
          },
          { id: 'litellm', label: 'LiteLLM', detail: 'Self-hosted proxy', requiresApiKey: true, kind: 'custom' },
          {
            id: 'cloudflare-aigateway',
            label: 'Cloudflare AI Gateway',
            detail: 'Cloudflare AI Gateway REST API (Unified Billing)',
            requiresApiKey: true,
            requiresGatewayId: true,
            requiresAccountId: true,
            kind: 'hosted',
          },
        ],
      }
    : base;

  return `snapshot\t${encodeBase64Utf8(JSON.stringify(snapshot))}`;
}

function route(clientId: string, client: string) {
  return {
    id: `act-rich-${clientId}`,
    clientId,
    sourceId: `account/${clientId}/work`,
    client,
    label: 'Work',
    detail: '',
    kind: 'native',
    presentation: 'direct-account',
    selected: false,
    enabled: true,
    routeKind: 'native',
  };
}
