/**
 * `AccountsView`'s provider filter, ported from `src/macos/AccountsView.swift`
 * and `DesignTypes.swift:490`.
 *
 * The search box narrows by text across every provider; the filter narrows to
 * one. They are not the same control: a user with fourteen accounts across six
 * providers typing "work" wants the ones called Work from all six, and a user
 * who wants only Claude's wants the filter.
 */

import type { AccountRow as AccountRowContract, GatewayRow } from './contract';
import { nativeProviderName } from './model';

export type ProviderFilter = { kind: 'all' } | { kind: 'provider'; provider: string };

export const ALL_PROVIDERS: ProviderFilter = { kind: 'all' };

/** `nativeProviderPriority`. Native providers first, in the order users adopt. */
export function nativeProviderPriority(provider: string): number {
  const value = provider.toLowerCase();
  if (value.includes('claude')) return 0;
  if (value.includes('codex') || value.includes('openai')) return 1;
  if (value.includes('gemini') || value.includes('google')) return 2;
  if (value.includes('kiro')) return 3;
  if (value.includes('grok')) return 4;
  if (value.includes('opencode')) return 5;
  if (value.includes('workbuddy') || value.includes('codebuddy')) return 6;
  return 7;
}

/**
 * `providers`. The providers that have accounts, in adoption order.
 *
 * Sorted by priority and then by name, so the filter's list does not reshuffle
 * between two snapshots that differ only in which account was added.
 */
export function providerFilterOptions(
  accounts: readonly AccountRowContract[],
  gateways: readonly GatewayRow[],
): string[] {
  const names = new Set<string>();
  for (const account of accounts) names.add(account.providerId);
  for (const gateway of gateways) names.add(gateway.providerId);
  return [...names].sort((lhs, rhs) => {
    const left = nativeProviderPriority(lhs);
    const right = nativeProviderPriority(rhs);
    if (left !== right) return left - right;
    return lhs.localeCompare(rhs);
  });
}

/**
 * Whether a row survives the filter.
 *
 * An API gateway and a saved login for the same provider are both kept: the
 * filter names a provider, not a kind, and a user filtering to Cloudflare
 * wants the gateway row as much as the account.
 */
export function matchesProviderFilter(providerId: string, filter: ProviderFilter): boolean {
  return filter.kind === 'all' || filter.provider === providerId;
}

/** The filter's own label, so the empty state can say what is being hidden. */
export function providerFilterLabel(filter: ProviderFilter): string {
  return filter.kind === 'all' ? 'All Providers' : nativeProviderName(filter.provider);
}