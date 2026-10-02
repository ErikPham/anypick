/**
 * `OverviewView`'s selection rules, ported from `src/macos/OverviewView.swift`.
 *
 * The pane is a summary, not a list. macOS shows at most three apps and offers a
 * way to the rest; showing every app makes Overview a second Apps pane, and the
 * app that needs attention sinks below the ones that are fine.
 */

import type { ActionRow } from './contract';
import { isAppRouteConfigured, transportDisplayName } from './model';

/** One app as the Overview row reads it: one row per client, not per route. */
export type OverviewApp = {
  clientId: string;
  client: string;
  selected: ActionRow | null;
  /** The client needs the user: no usable route, or the engine said why not. */
  needsAttention: boolean;
};

/**
 * `hasActiveOverviewApps` / `overviewApps`.
 *
 * When something is already active or broken, those are the three rows worth
 * space; only when nothing is active does the pane fall back to the first three
 * it finds. A row per client, because an app with four routes is one app.
 */
export function overviewApps(actions: readonly ActionRow[]): {
  listed: OverviewApp[];
  total: number;
  hasMore: boolean;
  title: string;
  footer: string;
} {
  const byClient = new Map<string, OverviewApp>();
  for (const action of actions) {
    const current = byClient.get(action.clientId);
    const attention = Boolean(action.disabledReason);
    if (!current) {
      byClient.set(action.clientId, {
        clientId: action.clientId,
        client: action.client,
        selected: action.selected ? action : null,
        needsAttention: attention,
      });
      continue;
    }
    if (action.selected) current.selected = action;
    if (attention) current.needsAttention = true;
  }
  const all = [...byClient.values()];
  const active = all.filter((app) => app.selected !== null || app.needsAttention);
  const listed = (active.length > 0 ? active : all).slice(0, 3);
  return {
    listed,
    total: all.length,
    hasMore: all.length > listed.length,
    title: active.length > 0 ? 'Active Apps' : 'Apps',
    // A null selection is unconfigured by definition; `isAppRouteConfigured`
    // reads `.routeKind` off its argument and a null would throw here rather
    // than answering the question this line is asking.
    footer: all.some((app) => !app.selected || !isAppRouteConfigured(app.selected))
      ? 'Choose a login for each app before it can run.'
      : '',
  };
}

/**
 * `overviewConnectionSummary`. What the row says about the app's current login.
 *
 * "Uses <name>" rather than the bare name: the row's title is the app, so a
 * detail that is just a login name leaves the reader guessing what it is. An
 * app with no route says so, because "No login chosen" is what the row is
 * telling the user to fix.
 */
export function overviewConnectionSummary(app: OverviewApp): string {
  if (app.selected?.label) return `Uses ${app.selected.label}`;
  if (app.selected?.detail) return app.selected.detail;
  return 'No login chosen';
}

/**
 * The transport word on the trailing edge, in the same words the Apps pane uses.
 *
 * Two panes naming the same thing differently is how a user concludes they are
 * looking at two different routes.
 */
export function overviewTransportLabel(app: OverviewApp): string {
  if (!app.selected) return 'Not Configured';
  return transportDisplayName(app.selected.routeKind, isAppRouteConfigured(app.selected));
}

/**
 * `onboardingFooter`.
 *
 * Proxy Hub is optional, and saying so is what makes hiding the checklist an
 * informed choice rather than the loss of a required step.
 */
export function onboardingFooter(isCoreComplete: boolean): string {
  return isCoreComplete
    ? 'Proxy Hub is optional — you can hide this list.'
    : 'Save a login, then point an app at it. Proxy Hub is optional.';
}

/**
 * `isOnboardingCoreComplete`.
 *
 * Core is a saved login and an app pointed at one. The Hub is the optional
 * third step, so completing those two is what completes setup.
 */
export function isOnboardingCoreComplete(input: {
  accounts: readonly unknown[];
  actions: readonly ActionRow[];
}): boolean {
  return input.accounts.length > 0 && input.actions.some((action) => action.selected);
}