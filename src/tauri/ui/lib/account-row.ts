/**
 * The account row's shape, its trailing grid, and its commands.
 *
 * Ported from `AccountRowTrailing` in `src/macos/AccountsView.swift:368`.
 *
 * The column widths are the contract: the action column holds the widest
 * control it can ("Connect"), the marker column one symbol, the chevron column
 * the drill-in disclosure. Every column holds its width whether or not this row
 * fills it, which is what makes the trailing controls line up down the list.
 */

import type { AccountRow, GatewayRow } from './contract';
import { accountStatus, cleanSourceDisplayName, type DesignStatus } from './model';

export const COLUMN_WIDTH = {
  /** Wide enough for "Connect" at 12px plus its bezel. */
  action: 'w-[72px]',
  /** One SF Symbol. */
  status: 'w-[18px]',
  /** The 12pt chevron. */
  chevron: 'w-[14px]',
  /** The 22px ellipsis disclosure. */
  menu: 'w-[26px]',
} as const;

/**
 * One account or gateway, normalized.
 *
 * The engine sends two shapes with almost nothing in common: a saved account has
 * `active`, `switchActionId` and `canRefresh`; a gateway has `ready` and a
 * default model and none of those. Passing the raw union around made every call
 * site narrow by hand and one of them forgot, which reads `active` off a
 * gateway and marks it attention forever.
 *
 * `kind` is the discriminator. Building this once, here, is what makes that
 * mistake impossible rather than unlikely.
 */
export type AccountRowView = {
  id: string;
  providerId: string;
  kind: 'account' | 'gateway';
  name: string;
  label: string;
  detail: string;
  status: DesignStatus;
  /** A saved login no app currently uses. False for every gateway. */
  active: boolean;
  /** The Switch action, or empty when there is nothing to switch. */
  switchActionId: string;
  /** Whether this row may be refreshed or tested. */
  canRefresh: boolean;
};

export function accountView(account: AccountRow): AccountRowView {
  return {
    id: account.id,
    providerId: account.providerId,
    kind: 'account',
    name: account.name,
    label: account.label || account.name,
    // The engine's value, unfilled. `accountRowSubtitle` distinguishes a blank
    // detail ("Saved login") from a real one, and back-filling the name here
    // would make every blank account read "Claude · work".
    detail: account.detail,
    status: accountStatus(account),
    active: account.active,
    switchActionId: account.switchActionId ?? '',
    canRefresh: account.canRefresh ?? false,
  };
}

export function gatewayView(gateway: GatewayRow): AccountRowView {
  return {
    id: gateway.id,
    providerId: gateway.providerId,
    kind: 'gateway',
    name: gateway.name,
    label: gateway.name,
    detail: gateway.detail,
    // A gateway is ready or it is not. It has no `active` field, and reading
    // one would classify every gateway as attention.
    status: gateway.ready ? 'ready' : 'attention',
    active: false,
    switchActionId: '',
    canRefresh: gateway.ready,
  };
}

/**
 * `showsAction`. Whether the primary column has anything to reveal.
 *
 * A saved account that is already current has no Switch to offer, and an
 * unavailable gateway has no Connect. Revealing an empty cell would be the
 * width bug macOS documented.
 */
export function showsPrimaryAction(row: AccountRowView): boolean {
  // A gateway that is not ready has nothing to connect to, so the column holds
  // nothing. Testing for `unavailable` here would never fire: a gateway's
  // status is ready or attention, and attention is exactly the case to catch.
  if (row.kind === 'gateway') return row.status === 'ready';
  return !row.active;
}

/**
 * `accountRowSubtitle`. `Provider · identity`, or the kind alone.
 *
 * The identity is dropped when it merely repeats the provider: "Claude · Claude"
 * is noise, and the group heading already names the provider.
 */
export function accountRowSubtitle(row: AccountRowView): string {
  const detail = row.detail.trim();
  let identity = '';
  if (detail) {
    const resolved = cleanSourceDisplayName(detail);
    // Case-insensitive: the engine may echo the provider in a different case.
    identity = resolved.toLocaleLowerCase() === row.providerId.toLocaleLowerCase() ? '' : resolved;
  }
  if (row.kind === 'gateway') return identity === '' ? 'API Gateway' : identity;
  const provider = providerShort(row.providerId);
  return identity === '' ? `${provider} · Saved login` : `${provider} · ${identity}`;
}

/** The provider word in a subtitle, short. The group heading carries the long one. */
function providerShort(providerId: string): string {
  const short: Record<string, string> = {
    claude: 'Claude',
    anthropic: 'Claude',
    openai: 'OpenAI',
    gemini: 'Gemini',
    kiro: 'Kiro',
    openrouter: 'OpenRouter',
    opencode: 'OpenCode',
    grok: 'Grok',
    workbuddy: 'WorkBuddy',
    zai: 'Z.ai',
    cline: 'Cline',
  };
  return short[providerId.toLowerCase()] ?? providerId;
}

/** The row's issue text, or nothing. macOS returns nil for every ready state. */
export function accountIssue(row: AccountRowView): string | undefined {
  return row.status === 'attention' || row.status === 'unavailable' ? row.status : undefined;
}

/** One item in the row menu, ordered as macOS orders it. */
export type RowCommand = {
  id: 'details' | 'edit' | 'test' | 'switch' | 'connect' | 'remove';
  title: string;
  destructive?: boolean;
  disabled?: boolean;
};

/**
 * The menu's contents.
 *
 * Switch and Connect live in the primary column too, because a keyboard user
 * never hovers and a command that exists only on hover does not exist for them.
 * A command in two places is a command that can disagree with itself, so both
 * call the same handler.
 */
export function rowCommands(row: AccountRowView, busy: boolean): RowCommand[] {
  // Show Details stays enabled while the engine is busy. It only reads the
  // snapshot, and disabling it would make a row's own drill-in look broken
  // during the one moment the user most wants to read it.
  const commands: RowCommand[] = [{ id: 'details', title: 'Show Details' }];
  if (row.kind === 'gateway') {
    commands.push({ id: 'connect', title: 'Connect', disabled: busy || !showsPrimaryAction(row) });
  } else if (!row.active) {
    commands.push({ id: 'switch', title: 'Switch to Account', disabled: busy || !row.switchActionId });
  }
  commands.push({ id: 'edit', title: 'Edit…', disabled: busy });
  commands.push({
    id: 'test',
    title: row.kind === 'gateway' ? 'Test Connection' : 'Refresh Login',
    disabled: busy || !row.canRefresh,
  });
  commands.push({ id: 'remove', title: 'Remove…', destructive: true, disabled: busy });
  return commands;
}

/**
 * The removal dialog's message. Three consequences, said differently, because
 * "this will no longer be available" does not tell a user whether the API key
 * they created is still live at the provider.
 */
export function removalMessage(row: AccountRowView): string {
  if (row.kind === 'gateway') {
    return `Only ${row.label} is removed from AnyPick. The API key at the provider is not revoked.`;
  }
  if (row.active) {
    return `${row.label} is removed from AnyPick. The app stays signed in on this Mac until it is changed there.`;
  }
  return `${row.label} is removed from AnyPick. Your login does not change.`;
}