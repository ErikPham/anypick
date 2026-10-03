/**
 * `AppsView`'s row model, ported from `src/macos/AppsView.swift`.
 *
 * The shape that made this pane look like a control panel was building **a row
 * per route**. An app with four saved logins became four rows, each with its own
 * Use, Change…, Refresh, Disconnect and Remove — so the pane read as a grid of
 * buttons rather than a list of apps.
 *
 * macOS builds **one row per app**. The row says which route is in use; the
 * commands that act on the whole app live in its context menu. That is the
 * layout this ports, and the reason the pane has fewer controls on screen.
 */

import type { ActionRow, ClientModelConfig, Snapshot } from './contract';
import { isAppManagedByAnyPick, isAppRouteConfigured, transportDisplayName } from './model';

/** One app, collapsed from every route it publishes. */
export type AppEntry = {
  clientId: string;
  client: string;
  routes: ActionRow[];
  /** The route in use, or null when the app has none. */
  selected: ActionRow | null;
  /** AnyPick routes this app through something it owns. */
  managed: boolean;
  /** A row the engine disabled, with the reason it gave. */
  issue?: string;
};

/**
 * `filteredApps` then one row per client.
 *
 * `disabledReason` wins over `selected` for the *issue* only: a selected route
 * can still be refused later, and a user needs the reason next to the app it
 * belongs to rather than on whichever row happened to carry it.
 */
export function appEntries(actions: readonly ActionRow[]): AppEntry[] {
  const byClient = new Map<string, ActionRow[]>();
  for (const action of actions) {
    const key = action.clientId || action.client;
    byClient.set(key, [...(byClient.get(key) ?? []), action]);
  }
  return [...byClient.entries()].map(([clientId, routes]) => ({
    clientId,
    client: routes[0]?.client ?? clientId,
    routes,
    selected: routes.find((route) => route.selected) ?? null,
    managed: routes.some((route) => isAppManagedByAnyPick(route)),
    issue: routes.find((route) => route.disabledReason)?.disabledReason,
  }));
}

/**
 * `appSubtitle`. What the row says about the app's current login.
 *
 * The route's own name first, then its detail when the detail says something
 * the name does not. A detail that merely repeats "Direct" or "Managed" beside a
 * label already saying so is noise, so those are dropped.
 */
export function appSubtitle(app: AppEntry): string {
  const route = app.selected;
  if (!route) return 'Not Configured';
  const name = route.label || (app.client ? `Connected to ${app.client}` : 'Connected');
  const parts = [name];
  const detail = route.detail?.trim() ?? '';
  if (detail && detail !== name && !/^(Direct|Managed|Login managed|Uses )/.test(detail)) {
    parts.push(detail);
  } else if (route.detail?.startsWith('Uses ')) {
    // "Uses Work · alex@studio.example" already names the route; the second
    // half is the identity, which is worth keeping and the label is not.
    for (const part of route.detail.slice('Uses '.length).split(' · ')) {
      if (part && !parts.includes(part)) parts.push(part);
    }
  }
  return parts.join(' · ');
}

export function appIssue(app: AppEntry): string | undefined {
  if (app.issue) return app.issue;
  if (app.selected && app.selected.disabledReason) return app.selected.disabledReason;
  if (!app.selected) return 'No login chosen for this app.';
  return undefined;
}

/**
 * The trailing value.
 *
 * A managed app shows its status. An unmanaged one shows what it would take:
 * "Set Up…" when AnyPick could route it and "Direct" when the app's own login
 * is what it is using. A bare row of words is not the macOS contract — the
 * trailing column is where the row says what to do next.
 */
export function appTrailing(app: AppEntry): {
  kind: 'status' | 'setup' | 'direct';
  label: string;
} {
  if (app.managed && app.selected) {
    const configured = isAppRouteConfigured(app.selected);
    return configured
      ? { kind: 'status', label: transportDisplayName(app.selected.routeKind, true) }
      : { kind: 'setup', label: 'Set Up…' };
  }
  if (app.selected?.routeKind === 'native' && app.selected.selected) return { kind: 'direct', label: 'Direct' };
  return { kind: 'setup', label: 'Set Up…' };
}

/** The section header, which counts so the user knows what a search narrowed. */
export function appsSectionTitle(count: number, searching: boolean): string {
  return searching ? `Matching Apps (${count})` : `Apps (${count})`;
}

/** The model config the app's Manage Models row opens, if the engine published one. */
export function appModelConfig(snapshot: Snapshot, clientId: string): ClientModelConfig | null {
  return (
    snapshot.clientModelConfigs.find(
      (config) =>
        config.clientId === clientId &&
        (config.options.length > 0 || config.editable || Boolean(config.unavailableReason)),
    ) ?? null
  );
}