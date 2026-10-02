/**
 * Which apps the user manages, ported from `src/macos/AppPreferences.swift` and
 * `MacOSDesignModel+Apps.swift`.
 *
 * The engine publishes every registered client, so "which apps are mine" is a
 * local preference and nothing else — macOS keeps it in `UserDefaults`, not in
 * supervisor state, and that placement is load-bearing: an app the user removed
 * must not reappear because the service restarted.
 *
 * The Tauri pane listed every published client. That is not the same list, and
 * the difference matters: a user who removed an app saw it come back on the next
 * launch, and a user with eleven supported tools had eleven rows before adding
 * anything.
 */

import type { ActionRow } from './contract';

const CONFIGURED_KEY = 'anypick.configuredApps';
const REMOVED_KEY = 'anypick.removedApps';

/** The apps macOS shows on a first run when the engine says nothing. */
const FIRST_RUN_DEFAULTS = ['claude', 'codex', 'kiro'];

/**
 * `effectiveConfiguredAppIDs`.
 *
 * With no stored choice, an app is included when AnyPick is already managing
 * it, it has a route, or it is ready — so a first run after configuring one
 * login does not drop every other app off the list. With nothing at all, three
 * familiar names, so the list is never empty on first run.
 */
export function effectiveConfiguredAppIDs(input: {
  allAppIds: readonly string[];
  actions: readonly ActionRow[];
  stored: readonly string[] | null;
  removed: readonly string[];
}): string[] {
  const removed = new Set(input.removed);
  const configured =
    input.stored ??
    (() => {
      const initial = new Set(
        input.actions.filter((action) => isManagedOrConfigured(action)).map((action) => action.clientId),
      );
      if (initial.size === 0) {
        for (const id of FIRST_RUN_DEFAULTS) {
          if (input.allAppIds.includes(id)) initial.add(id);
        }
      }
      return [...initial];
    })();
  return configured.filter((id) => !removed.has(id) && input.allAppIds.includes(id));
}

/**
 * `isAppManagedByAnyPick(app) || isAppRouteConfigured(app)`.
 *
 * Two halves, and the first one excluding `native` is the whole point. Every
 * registered client publishes a native row whether or not AnyPick has ever
 * touched it, so treating "has a routeKind" as "the user manages this app"
 * classifies every supported app as already added — and Add App offers nothing,
 * on every first run, forever.
 *
 * The second half keeps a *selected* native row, because the user chose it.
 */
function isManagedOrConfigured(action: ActionRow): boolean {
  if (!action.routeKind) return false;
  if (action.selected) return true;
  return action.routeKind !== 'native';
}

/** `availableAppsToAdd`. The supported apps not yet on the list. */
export function availableAppsToAdd(allAppIds: readonly string[], configured: readonly string[]): string[] {
  return allAppIds.filter((id) => !configured.includes(id));
}

/**
 * `addApp`. The next configured set and the next removed set.
 *
 * Re-adding an app clears its removal, because `effectiveConfiguredAppIDs`
 * subtracts removals: leaving the removal behind would make "add it back"
 * succeed on screen and show nothing, which is the one outcome the user cannot
 * diagnose from the pane.
 */
export function addApp(
  configured: readonly string[],
  removed: readonly string[],
  appId: string,
): { configured: string[]; removed: string[] } {
  return {
    configured: configured.includes(appId) ? [...configured] : [...configured, appId],
    removed: removed.filter((id) => id !== appId),
  };
}

/**
 * `removeApp`.
 *
 * Removal is remembered separately from the configured list so that
 * `effectiveConfiguredAppIDs` can subtract it: a single stored list cannot
 * express "never added this" and "added then removed".
 */
export function removeApp(
  configured: readonly string[],
  removed: readonly string[],
  appId: string,
): { configured: string[]; removed: string[] } {
  return {
    configured: configured.filter((id) => id !== appId),
    removed: removed.includes(appId) ? [...removed] : [...removed, appId],
  };
}

/** `appShortDescription`. One line saying what the app is. */
export function appShortDescription(clientId: string): string {
  switch (clientId.toLowerCase()) {
    case 'claude':
      return 'Anthropic’s terminal coding agent';
    case 'codex':
      return 'OpenAI’s terminal and desktop coding tool';
    case 'kiro':
      return 'AI-assisted coding IDE and environment';
    case 'commandcode':
      return 'Terminal command AI assistant';
    case 'gemini':
      return 'Google’s AI coding tool';
    case 'opencode':
      return 'Open-source developer terminal assistant';
    case 'workbuddy':
    case 'codebuddy':
      return 'Tencent’s AI programming assistant';
    case 'zcode':
      return 'Z.ai intelligent development environment';
    case 'dsh':
      return 'DeepSeek AI coding harness';
    case 'pi':
      return 'Interactive terminal AI agent';
    case 'omp':
      return 'Multi-provider AI command line interface';
    default:
      return 'Supported coding application';
  }
}

function readList(key: string): string[] | null {
  try {
    const raw = globalThis.localStorage?.getItem(key);
    if (raw === null || raw === undefined) return null;
    const parsed = JSON.parse(raw) as unknown;
    return Array.isArray(parsed) ? parsed.filter((id): id is string => typeof id === 'string') : null;
  } catch {
    // Unreadable storage is not an error worth failing a pane over; the derived
    // first-run list is a correct answer.
    return null;
  }
}

function writeList(key: string, value: readonly string[]): void {
  try {
    globalThis.localStorage?.setItem(key, JSON.stringify([...value]));
  } catch {
    // A preference that cannot be stored still applies for this session.
  }
}

export function storedConfiguredAppIDs(): string[] | null {
  return readList(CONFIGURED_KEY);
}

export function storedRemovedAppIDs(): string[] {
  return readList(REMOVED_KEY) ?? [];
}

export function storeConfiguredAppIDs(value: readonly string[]): void {
  writeList(CONFIGURED_KEY, value);
}

export function storeRemovedAppIDs(value: readonly string[]): void {
  writeList(REMOVED_KEY, value);
}