import { describe, expect, it } from 'vitest';

import type {
  AccountRow as AccountRowContract,
  GatewayRow,
} from '../lib/contract';
import {
  COLUMN_WIDTH,
  accountIssue,
  accountRowSubtitle,
  accountView,
  gatewayView,
  removalMessage,
  rowCommands,
  showsPrimaryAction,
} from '../lib/account-row';

/**
 * The account row's view model and its trailing grid.
 *
 * The normalization matters more than the arithmetic here. The engine sends two
 * shapes with almost nothing in common — a saved account has `active`,
 * `switchActionId` and `canRefresh`; a gateway has `ready` and none of those —
 * and passing the raw union around made one call site read `active` off a
 * gateway, which is undefined, which classifies it as attention forever.
 */

function account(
  overrides: Partial<AccountRowContract> = {},
): AccountRowContract {
  return {
    id: 'claude/work',
    providerId: 'claude',
    sourceId: 'account/claude/work',
    name: 'work',
    label: 'Work',
    detail: 'alex@studio.example',
    active: true,
    canRefresh: true,
    switchActionId: 'act-switch',
    ...overrides,
  } as AccountRowContract;
}

function gateway(overrides: Partial<GatewayRow> = {}): GatewayRow {
  return {
    id: 'team',
    providerId: 'openrouter',
    name: 'team',
    detail: 'Team gateway',
    ready: true,
    defaultModel: 'openai/gpt-4o',
    ...overrides,
  };
}

describe('normalizing the two row shapes', () => {
  it('reads a saved account', () => {
    const row = accountView(account());
    expect(row.kind).toBe('account');
    expect(row.status).toBe('ready');
    expect(row.active).toBe(true);
    expect(row.switchActionId).toBe('act-switch');
    expect(row.label).toBe('Work');
  });

  it('reads a gateway without borrowing the account fields', () => {
    // A gateway has no `active` and no `switchActionId`. Reading them anyway
    // would mark every gateway as attention and offer a Switch with nothing
    // behind it.
    const row = gatewayView(gateway());
    expect(row.kind).toBe('gateway');
    expect(row.active).toBe(false);
    expect(row.switchActionId).toBe('');
    expect(row.status).toBe('ready');
  });

  it('reads a gateway that is not ready as needing attention', () => {
    expect(gatewayView(gateway({ ready: false })).status).toBe('attention');
  });

  it('falls back to the name when the label is blank', () => {
    expect(accountView(account({ label: '' })).label).toBe('work');
  });
});

describe('the row subtitle', () => {
  it('names the provider and the identity', () => {
    expect(accountRowSubtitle(accountView(account()))).toBe(
      'Claude · alex@studio.example',
    );
  });

  it('says saved login when the detail is blank', () => {
    expect(accountRowSubtitle(accountView(account({ detail: '' })))).toBe(
      'Claude · Saved login',
    );
  });

  it('drops an identity that merely repeats the provider', () => {
    // "Claude · Claude" is noise, and the group heading already says Claude.
    expect(accountRowSubtitle(accountView(account({ detail: 'Claude' })))).toBe(
      'Claude · Saved login',
    );
  });

  it('compares case-insensitively when dropping the repeat', () => {
    expect(accountRowSubtitle(accountView(account({ detail: 'claude' })))).toBe(
      'Claude · Saved login',
    );
  });

  it('says API Gateway for a gateway with no identity', () => {
    expect(accountRowSubtitle(gatewayView(gateway({ detail: '' })))).toBe(
      'API Gateway',
    );
  });

  it('uses the identity alone for a gateway, without the provider word', () => {
    // A gateway's provider is its own name, so repeating it reads as a stutter.
    expect(
      accountRowSubtitle(gatewayView(gateway({ detail: 'sk-lab-1' }))),
    ).toBe('sk-lab-1');
  });
});

describe('the issue line', () => {
  it('says nothing for a healthy row', () => {
    expect(accountIssue(accountView(account()))).toBeUndefined();
    expect(accountIssue(gatewayView(gateway()))).toBeUndefined();
  });

  it('names the state for an unhealthy one', () => {
    expect(accountIssue(gatewayView(gateway({ ready: false })))).toBe(
      'attention',
    );
  });
});

describe('the primary column', () => {
  it('offers Switch for a saved account no app uses', () => {
    expect(showsPrimaryAction(accountView(account({ active: false })))).toBe(
      true,
    );
  });

  it('offers nothing for the account already in use', () => {
    // Revealing an empty cell is the width bug macOS documented.
    expect(showsPrimaryAction(accountView(account({ active: true })))).toBe(
      false,
    );
  });

  it('offers Connect for a ready gateway', () => {
    expect(showsPrimaryAction(gatewayView(gateway()))).toBe(true);
  });
});

describe('the row menu', () => {
  it('carries Switch as well as the primary button', () => {
    // A keyboard user never hovers, so a command that exists only in the
    // primary column does not exist for them.
    const ids = rowCommands(accountView(account({ active: false })), false).map(
      (c) => c.id,
    );
    expect(ids).toContain('switch');
  });

  it('omits Switch for the account already in use', () => {
    const ids = rowCommands(accountView(account()), false).map((c) => c.id);
    expect(ids).not.toContain('switch');
  });

  it('offers Connect on a gateway and Refresh Login never', () => {
    const titles = rowCommands(gatewayView(gateway()), false).map(
      (c) => c.title,
    );
    expect(titles).toContain('Connect');
    expect(titles).toContain('Test Connection');
    expect(titles).not.toContain('Refresh Login');
  });

  it('ends on Remove, marked destructive, after a divider', () => {
    // A destructive item next to a live one is a mis-click waiting to happen.
    const commands = rowCommands(accountView(account()), false);
    const last = commands[commands.length - 1];
    expect(last?.id).toBe('remove');
    expect(last?.destructive).toBe(true);
  });

  it('disables every command that writes, while leaving the read one open', () => {
    // Show Details only reads the snapshot. Disabling it would make a row's own
    // drill-in look broken during the one moment the user most wants to read it.
    const commands = rowCommands(accountView(account()), true);
    expect(
      commands.filter((c) => c.id !== 'details').every((c) => c.disabled),
    ).toBe(true);
    expect(commands.find((c) => c.id === 'details')?.disabled).toBeFalsy();
  });

  it('disables Switch for an account with no switch action', () => {
    const row = accountView(account({ active: false, switchActionId: '' }));
    expect(
      rowCommands(row, false).find((c) => c.id === 'switch')?.disabled,
    ).toBe(true);
  });

  it('disables Connect for an unavailable gateway', () => {
    const row = gatewayView(gateway({ ready: false }));
    expect(
      rowCommands(row, false).find((c) => c.id === 'connect')?.disabled,
    ).toBe(true);
  });
});

describe('the removal message', () => {
  it('says the key at the provider survives', () => {
    // "This will no longer be available" does not tell a user whether the API
    // key they created is still live.
    expect(removalMessage(gatewayView(gateway()))).toContain('not revoked');
  });

  it('says the app stays signed in for the current account', () => {
    expect(removalMessage(accountView(account({ active: true })))).toContain(
      'stays signed in',
    );
  });

  it('says the login does not change otherwise', () => {
    expect(removalMessage(accountView(account({ active: false })))).toContain(
      'does not change',
    );
  });

  it('gives all three a different sentence', () => {
    // Three states, said differently, because they call for different actions.
    const messages = [
      removalMessage(gatewayView(gateway())),
      removalMessage(accountView(account({ active: true }))),
      removalMessage(accountView(account({ active: false }))),
    ];
    expect(new Set(messages).size).toBe(3);
  });
});

describe('the column widths', () => {
  it('are distinct, so the columns read as a grid', () => {
    const widths = Object.values(COLUMN_WIDTH);
    expect(new Set(widths).size).toBe(widths.length);
  });

  it('gives the action column the most room, because it holds "Connect"', () => {
    expect(COLUMN_WIDTH.action).toContain('72');
    expect(COLUMN_WIDTH.status).toContain('18');
    expect(COLUMN_WIDTH.chevron).toContain('14');
  });

  it('puts the width on the column, not on the control inside it', () => {
    // The macOS trap: `Group { if … }.frame(width:)` drops the frame, so a row
    // with no primary command slid left and the trailing controls stopped lining
    // up. The width belongs to the column element so that it holds whether or
    // not the column has anything in it.
    for (const width of Object.values(COLUMN_WIDTH)) {
      expect(width).toMatch(/^w-\[/);
    }
    const column = (inner: string) =>
      `<span class="flex flex-none items-center justify-end ${COLUMN_WIDTH.action}">${inner}</span>`;
    expect(column('')).toBe(
      column('<button>Switch</button>').replace('<button>Switch</button>', ''),
    );
  });
});
