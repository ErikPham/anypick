import type { ClientId, SourceAdapter, TransportCapability, TransportPolicy } from '../types';
import { anypickError, ExitCode } from '../utils/errors';

const MANAGED_PROXY = new Set<TransportCapability>([
  'managed_builtin_proxy',
  'managed_external_proxy',
  'external_manual_proxy',
]);

/**
 * Resolve an adapter's provider-owned transport offer against explicit user
 * intent. `auto` preserves the adapter default. Explicit policy never falls
 * back to another path: a direct request either resolves to direct or fails.
 */
export function resolveTransportPolicy(
  adapter: SourceAdapter,
  clientId: ClientId,
  policy: TransportPolicy,
): TransportCapability {
  const automatic = adapter.transportFor(clientId);
  if (policy === 'auto') {
    return automatic;
  }
  const selected = adapter.transportForPolicy?.(clientId, policy) ?? automatic;
  if (policy === 'direct' && selected === 'direct') {
    return selected;
  }
  if (policy === 'proxy' && MANAGED_PROXY.has(selected)) {
    return selected;
  }

  const wanted = policy === 'direct' ? 'direct connection' : 'local proxy';
  throw anypickError(
    `Source ${adapter.sourceRef.kind} does not support ${wanted} for client ${clientId}.`,
    'UNSUPPORTED_TRANSPORT',
    {
      exitCode: ExitCode.CAPABILITY_CONFLICT,
      suggestions: [
        policy === 'direct'
          ? 'Use --transport auto or proxy, or choose a compatible direct endpoint.'
          : 'Enable a supported local proxy for this source, or use --transport auto/direct.',
      ],
    },
  );
}
