// Emits one desktop snapshot frame, in the exact wire form the Go service
// sends: `snapshot\t<base64 of UTF-8 JSON>`.
//
// The Tauri app's fixture mode (ANYPICK_TRAY_DEMO) reads that from stdin, so
// the same snapshot the unit tests render can be pushed into the real window
// with no Go service and no access to ~/.anypick. This is how the UI is
// looked at during review, and it reuses test/fixture.ts so the preview can
// never drift from what the tests assert on.
//
//   npm run fixture:line | ANYPICK_TRAY_DEMO=1 ../app/target/debug/anypick
//
// `--rich` widens the data instead. The default fixture publishes exactly one
// client, which means the sheets that branch on provider or on app list can
// only ever be seen in their empty branch — a sheet that renders correctly with
// no rows proves nothing about the rows. `--rich` adds the clients and gateway
// providers those branches need, so a screenshot can actually reach them.

import { snapshotFixture } from '../test/fixture.ts';
import { encodeBase64Utf8 } from '../lib/wire.ts';

const base = snapshotFixture();
const rich = process.argv.includes('--rich');

const snapshot = rich
  ? {
      ...base,
      // Clients AnyPick has no route for. These are what "Add App…" offers, so
      // the sheet's populated branch is reachable at all.
      actions: [
        ...base.actions,
        route('gemini', 'Gemini', false),
        route('opencode', 'OpenCode', false),
        route('kiro', 'Kiro', false),
      ],
      // The Add Account provider popup, opened, is a list of brand marks — the
      // whole reason `DesignProviderPicker` bridges `NSPopUpButton`. One entry
      // cannot show whether the marks render or whether the list scrolls.
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
      // One of each gateway shape: a provider that supplies its own endpoint,
      // one that has no endpoint of its own, and one that derives the URL from
      // an account id. The gateway sheet asks for a different field per shape.
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

function route(clientId: string, client: string, selected: boolean) {
  return {
    id: `act-rich-${clientId}`,
    clientId,
    sourceId: `account/${clientId}/work`,
    client,
    label: 'Work',
    detail: '',
    kind: 'native',
    presentation: 'direct-account',
    selected,
    enabled: true,
    routeKind: 'native',
  };
}

process.stdout.write(`snapshot\t${encodeBase64Utf8(JSON.stringify(snapshot))}\n`);