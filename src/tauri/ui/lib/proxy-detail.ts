import type { AccountRow, ActionRow, DegradedCondition, ProxyRow } from './contract';
import { accountIdentityLine, nativeProviderName, servicePort } from './model';

/**
 * `ProxyDetailView`'s derivations, ported from
 * `src/macos/ProxyDetailView.swift:226-300`.
 *
 * They live here rather than in the sheet because each one is a rule about what
 * the engine actually sent, and a rule that is only visible in a screenshot is a
 * rule nobody can check. The macOS originals are private computed properties;
 * these are the same expressions, named.
 */

/** `proxy.port`, which the engine never sends. It is inside the address. */
export function proxyPort(proxy: ProxyRow): number {
  if (proxy.address) return servicePort(proxy.address) ?? 0;
  return servicePort(proxy.detail) ?? 0;
}

/** `purpose`: what this relay is *for*, as opposed to what it is called. */
export function proxyPurpose(proxy: ProxyRow): string {
  return `${nativeProviderName(proxy.providerId)} compatibility`;
}

/**
 * `servingAccountDisplay`. Which saved login the relay fronts.
 *
 * The engine puts the account in `detail` as `"{name} · {identity}"`, and the
 * relay's own `id` is `"{provider}/{name}"` — so the account can be resolved
 * either way. macOS tries the detail's first segment, then a substring match on
 * the id, and returns the name alone when neither account exists.
 */
export function servingAccountLabel(proxy: ProxyRow, accounts: readonly AccountRow[]): string {
  const detail = proxy.detail.trim();
  const separator = detail.indexOf(' · ');
  const fromDetail = (separator === -1 ? detail : detail.slice(0, separator)).trim();
  const name = fromDetail || detail;

  const match = accounts.find(
    (account) =>
      account.name.toLocaleLowerCase() === name.toLocaleLowerCase() ||
      proxy.id.toLocaleLowerCase().includes(account.id.toLocaleLowerCase()),
  );
  if (!match) return name || proxy.detail;

  const identity = accountIdentityLine(match.providerId, match.detail);
  return identity ? `${match.name} (${identity})` : match.name;
}

/**
 * `connectedApps`. Which apps route through this relay.
 *
 * Two conditions, and both matter: the app's **current** route must not be its
 * native one (an app AnyPick manages still has a native route published, so
 * dropping this test lists every app in the app), and the route must belong to
 * this proxy's provider (a Claude Code pointed at a Gemini relay is not a client
 * of the Gemini relay).
 */
export function proxyClientApps(
  proxy: ProxyRow,
  actions: readonly ActionRow[],
): { clientId: string; client: string; routeId: string }[] {
  const provider = proxy.providerId.toLocaleLowerCase();
  const seen = new Set<string>();
  const apps: { clientId: string; client: string; routeId: string }[] = [];
  for (const action of actions) {
    if (!action.selected || seen.has(action.clientId)) continue;
    if ((action.routeKind ?? action.kind) === 'native') continue;
    const route = `${action.id} ${action.sourceId}`.toLocaleLowerCase();
    if (action.clientId.toLocaleLowerCase() !== provider && !route.includes(provider)) continue;
    seen.add(action.clientId);
    apps.push({ clientId: action.clientId, client: action.client, routeId: action.id });
  }
  return apps;
}

/**
 * `model.serviceFailures[proxy.id]`. The failure that belongs to this relay.
 *
 * macOS keeps a per-id dictionary its own handlers write. The engine publishes
 * degraded conditions instead, and a proxy's failure is the one whose `id`
 * mentions the relay — matched on the provider/name pair rather than on the
 * kind, because `proxy`, `gateway` and `hub` conditions all arrive here and only
 * one of them is about this row.
 */
export function proxyFailure(
  proxy: ProxyRow,
  conditions: readonly DegradedCondition[],
): string | undefined {
  const id = proxy.id.toLocaleLowerCase();
  const provider = proxy.providerId.toLocaleLowerCase();
  const match = conditions.find((condition) => {
    const key = condition.id.toLocaleLowerCase();
    return key === id || key.includes(id) || (key.includes(provider) && key.includes('proxy'));
  });
  return match?.detail || match?.title || undefined;
}

/**
 * The verb for an in-flight command on this relay.
 *
 * macOS's `DesignServiceOperation` is `.starting` / `.stopping` / `.restarting`,
 * and the lifecycle button shows that word *inside* the button beside a spinner.
 * The action id is the only thing that says which verb: the engine publishes
 * `toggleActionId` / `restartActionId` / `rotateActionId` and no status field.
 */
export function proxyOperation(
  proxy: ProxyRow,
  inFlight: readonly { kind: string; actionId?: string; operation?: string }[],
): { verb: 'Starting' | 'Stopping' | 'Restarting'; rotate: boolean } | undefined {
  for (const flight of inFlight) {
    if (flight.actionId && flight.actionId === proxy.rotateActionId) return { verb: 'Restarting', rotate: true };
    if (flight.actionId && flight.actionId === proxy.restartActionId) return { verb: 'Restarting', rotate: false };
    if (flight.actionId && flight.actionId === proxy.toggleActionId) {
      return { verb: proxy.running ? 'Stopping' : 'Starting', rotate: false };
    }
  }
  return undefined;
}
