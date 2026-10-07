import { describe, expect, it } from 'vitest';

import { colourTokens, colourUtilities } from './inventory';

/**
 * Every colour a pane asks for has a token behind it.
 *
 * Tailwind v4 emits nothing for a colour utility whose `--color-*` token is
 * missing, so `bg-badge` and `bg-active` shipped as no-ops: the AnyPick proxy
 * badge rendered with no fill and the Settings button had no pressed state.
 * jsdom never loads the stylesheet, so no rendered test could see it. This
 * check reads the two sources directly, which is the only place the mismatch
 * is visible.
 */
describe('the colour tokens in styles.css', () => {
  const tokens = new Set(colourTokens());
  const utilities = colourUtilities();

  it('extracts a non-empty set from both sources', () => {
    // An extractor that matches nothing reads as success. Both halves must
    // produce a number, or the assertion below is vacuous.
    expect(utilities.length).toBeGreaterThan(0);
    expect(tokens.size).toBeGreaterThan(0);
  });

  it('defines a --color-* token for every colour utility in ui/**/*.tsx', () => {
    const missing = utilities.filter((name) => !tokens.has(name));

    expect(missing).toEqual([]);
  });

  it('names the three tokens that were missing before the fix', () => {
    // Pinned by name, because a future extractor change that stops seeing
    // `bg-badge` would otherwise let the class go undefined again.
    expect(utilities).toEqual(expect.arrayContaining(['badge', 'active', 'amber-500']));
    expect(tokens.has('badge')).toBe(true);
    expect(tokens.has('active')).toBe(true);
    expect(tokens.has('amber-500')).toBe(true);
  });
});
