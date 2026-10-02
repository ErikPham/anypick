import type { TrayBridge, BridgeEvent } from './bridge';
import { encodeBase64Utf8 } from './wire';
import { snapshotFixture } from '../test/fixture';

export function createBrowserDevBridge(): TrayBridge {
  const listeners: Record<string, Array<(event: BridgeEvent) => void>> = {};

  const snapshot = {
    ...snapshotFixture(),
    actions: [
      ...snapshotFixture().actions,
      {
        id: 'act-rich-gemini',
        clientId: 'gemini',
        sourceId: 'account/gemini/work',
        client: 'Gemini',
        label: 'Work',
        detail: '',
        kind: 'native',
        presentation: 'direct-account',
        selected: false,
        enabled: true,
        routeKind: 'native',
      },
      {
        id: 'act-rich-opencode',
        clientId: 'opencode',
        sourceId: 'account/opencode/work',
        client: 'OpenCode',
        label: 'Work',
        detail: '',
        kind: 'native',
        presentation: 'direct-account',
        selected: false,
        enabled: true,
        routeKind: 'native',
      },
      {
        id: 'act-rich-kiro',
        clientId: 'kiro',
        sourceId: 'account/kiro/work',
        client: 'Kiro',
        label: 'Work',
        detail: '',
        kind: 'native',
        presentation: 'direct-account',
        selected: false,
        enabled: true,
        routeKind: 'native',
      },
    ],
    accountProviders: [
      ...snapshotFixture().accountProviders,
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
  };

  const line = `snapshot\t${encodeBase64Utf8(JSON.stringify(snapshot))}`;
  let liveLine: string | null = null;
  let isLive = false;

  if (typeof window !== 'undefined' && typeof window.EventSource !== 'undefined') {
    try {
      const es = new EventSource('/_api/service-events');
      es.onmessage = (ev) => {
        try {
          const raw = JSON.parse(ev.data);
          if (typeof raw === 'string' && raw.length > 0) {
            liveLine = raw;
            isLive = true;
            listeners['service-line']?.forEach((cb) => cb({ payload: raw }));
          }
        } catch {}
      };
    } catch {}
  }

  return {
    invoke: async (cmd: string, args?: Record<string, unknown>) => {
      if (cmd === 'last_service_line') {
        if (liveLine) return liveLine;
        try {
          const res = await fetch('/_api/last-line');
          if (res.ok) {
            const data = await res.json();
            if (data?.line) {
              liveLine = data.line;
              isLive = true;
              return liveLine;
            }
          }
        } catch {}
        return line;
      }
      if (cmd === 'service_status') {
        return {
          phase: 'running',
          detail: isLive ? 'Connected to AnyPick Core' : 'Running in browser preview',
        };
      }
      if (cmd === 'zcode_context') {
        return { appInstalled: true, planWarning: null };
      }
      if (cmd === 'stage_auth_paste') {
        return '/tmp/anypick-auth-paste/mock.json';
      }
      if (cmd === 'forget_auth_paste') {
        return undefined;
      }
      if (cmd === 'send_command') {
        const command = args?.command as string | undefined;
        if (command && isLive) {
          try {
            await fetch('/_api/command', {
              method: 'POST',
              headers: { 'Content-Type': 'application/json' },
              body: JSON.stringify({ command }),
            });
            return undefined;
          } catch {}
        }
        if (command === 'refresh') {
          setTimeout(() => {
            listeners['service-line']?.forEach((cb) => cb({ payload: liveLine ?? line }));
          }, 50);
        }
        return undefined;
      }
      return undefined;
    },
    listen: async (event: string, handler: (event: BridgeEvent) => void) => {
      (listeners[event] ??= []).push(handler);
      if (event === 'service-state') {
        setTimeout(() => handler({ payload: 'running' }), 10);
      }
      if (event === 'service-line') {
        setTimeout(async () => {
          if (!liveLine) {
            try {
              const res = await fetch('/_api/last-line');
              if (res.ok) {
                const data = await res.json();
                if (data?.line) {
                  liveLine = data.line;
                  isLive = true;
                }
              }
            } catch {}
          }
          handler({ payload: liveLine ?? line });
        }, 20);
      }
      return () => {
        listeners[event] = (listeners[event] ?? []).filter((fn) => fn !== handler);
      };
    },
  };
}
