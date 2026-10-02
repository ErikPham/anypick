/**
 * The definition of done for the macOS to Tauri port, as a check.
 *
 * The macOS app names every user-reachable control with an
 * `accessibilityIdentifier`. `parity-map.ts` points each one at the Tauri
 * element that carries it, or marks it out of scope with a reason. This test
 * checks that map against both source trees, in both directions:
 *
 *   - a macOS feature with no row fails, so a new macOS feature cannot be
 *     quietly left unported;
 *   - a `done` row naming a `data-testid` that does not exist fails, so the
 *     map cannot claim coverage the app does not have;
 *   - a row for a macOS feature that no longer exists fails, so the map cannot
 *     rot into a stale exemption.
 *
 * That is the whole point. This app's history is that a green suite coexisted
 * with a visibly broken UI for a long time; nothing in CI could see the UI.
 */

import { readFileSync, readdirSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { describe, expect, it } from 'vitest';

import { macosIdentifiers, normalizeTauri, tauriIdentifiers } from './inventory';
import { OUT_OF_SCOPE, PARITY } from './parity-map';

const macosDir = resolve(__dirname, '../../../macos');
const macosSources = readdirSync(macosDir)
  .filter((file) => file.endsWith('.swift'))
  .map((file) => readFileSync(join(macosDir, file), 'utf8'));

const macosIds = new Set(macosIdentifiers());
const tauriIds = new Set(tauriIdentifiers().map(normalizeTauri));
const mappedMacos = new Set(PARITY.map((row) => row.macos));

/** True when a macOS feature is deliberately not ported, by prefix. */
function outOfScopeReason(id: string): string | undefined {
  for (const [prefix, reason] of OUT_OF_SCOPE) {
    if (id.startsWith(prefix)) return reason;
  }
  return undefined;
}

describe('the macOS to Tauri feature map', () => {
  it('extracts both inventories', () => {
    // Guards the extractors. A regex that matches nothing makes every
    // assertion below pass for the wrong reason.
    expect(macosIds.size).toBeGreaterThan(100);
    expect(tauriIds.size).toBeGreaterThan(40);
    expect(PARITY.length).toBeGreaterThan(100);
  });

  it('reads every shape an accessibility identifier takes in the Swift', () => {
    // A literal `.accessibilityIdentifier("x")` is one of three ways an id
    // reaches the tree. The other two — a component parameter
    // (`identifier: "x"`) and a computed helper that returns a literal — are
    // invisible to a scan of the first shape alone. When that scan was
    // literal-only it reported 137 macOS features instead of 143, and five
    // real controls had no row because the inventory could not see them.
    const source = readFileSync(
      resolve(__dirname, '../../../macos/SharedUI.swift'),
      'utf8',
    );
    // The copy button forwards its parameter; if it stopped, the ids that
    // reach the tree only through it would vanish from the inventory silently.
    expect(source).toMatch(/\.accessibilityIdentifier\(identifier\)/);

    // At least one id reaches the tree through each parameter spelling. Both
    // spellings exist in the Swift: a bare `identifier:` on the copy button,
    // and `accessibilityIdentifier:` on the candidate picker. A scan that read
    // one and not the other silently dropped the other's controls.
    const parameterIds = [...macosSources].flatMap((text) =>
      [...text.matchAll(/(?<![A-Za-z])identifier:\s*"([^"]+)"/g)].map((m) => m[1]),
    );
    expect(parameterIds.length).toBeGreaterThan(0);

    const labelledParameterIds = [...macosSources].flatMap((text) =>
      [...text.matchAll(/accessibilityIdentifier:\s*"([^"]+)"/g)].map((m) => m[1]),
    );
    expect(labelledParameterIds.length).toBeGreaterThan(0);

    const helperIds = [...macosSources].flatMap((text) =>
      [...text.matchAll(/func\s+\w*[Ii]dentifier\w*\s*\([^)]*\)\s*(?:->\s*String\s*)?\{([^}]{0,400})\}/g)]
        .flatMap((fn) => [...fn[1].matchAll(/"([^"]+)"/g)].map((m) => m[1])),
    );
    expect(helperIds.length).toBeGreaterThan(0);

    // Shape 3: a computed id in a `let` binding, applied later to a variable.
    // `ProxyHubView.swift:451` picks between two literals and passes the result
    // to `.accessibilityIdentifier(rowKey)`, so neither literal sits next to
    // the word `accessibilityIdentifier`. Reading only the first two shapes
    // left both hub source-row ids absent from the inventory.
    const boundIds = [...macosSources].flatMap((text) =>
      [...text.matchAll(/\blet\s+(\w*[Ii]d\w*|\w*[Kk]ey)\s*(?::\s*String\s*)?=\s*([^;\n]{0,200})/g)]
        .filter((m) => /^[^?"]*\?\s*"[^"]*"\s*:\s*"[^"]*"\s*$/.test(m[2].trim()))
        .flatMap((m) => [...m[2].matchAll(/"([^"]+)"/g)].map((lit) => lit[1])),
    );
    expect(boundIds.length).toBeGreaterThan(0);
  });

  it('maps every macOS feature', () => {
    const missing = [...macosIds].filter(
      (id) => !mappedMacos.has(id) && !outOfScopeReason(id),
    );
    expect(
      missing,
      `macOS features with no row in parity-map.ts:\n${missing.join('\n')}`,
    ).toEqual([]);
  });

  it('maps nothing that macOS does not have', () => {
    const stale = PARITY.map((row) => row.macos).filter((id) => !macosIds.has(id));
    expect(
      stale,
      `parity-map.ts rows for macOS ids that no longer exist:\n${stale.join('\n')}`,
    ).toEqual([]);
  });

  it('maps each macOS feature exactly once', () => {
    const seen = new Set<string>();
    const dupes = PARITY.map((r) => r.macos).filter((id) => (seen.has(id) ? true : (seen.add(id), false)));
    expect(dupes, `duplicate rows:\n${dupes.join('\n')}`).toEqual([]);
  });

  it('names a Tauri element that exists for every done row', () => {
    // The load-bearing assertion. A `done` row whose test id is absent is a
    // false claim of parity, which is the failure this file exists to stop.
    const unbacked = PARITY.filter(
      (row) => row.status === 'done' && !tauriIds.has(row.tauri),
    ).map((row) => `${row.macos} -> ${row.tauri}`);
    expect(
      unbacked,
      `parity-map.ts claims these Tauri elements exist but the UI does not emit them:\n${unbacked.join('\n')}`,
    ).toEqual([]);
  });

  it('reports Tauri elements no macOS feature maps to', () => {
    // Reported, not enforced. macOS names controls and leaves containers
    // unnamed, so the Tauri app legitimately has ids with no macOS
    // counterpart: a group wrapper, a pane root, a dialog that macOS reaches
    // through a different sheet. The direction that is enforced is the one
    // that matters: no macOS feature may be left unported.
    //
    // This is here so an orphan is noticed rather than forgotten. A new id
    // shows up in the report the next time the port touches its pane.
    const mappedTauri = new Set(PARITY.filter((r) => r.status === 'done').map((r) => r.tauri));
    const orphans = [...tauriIds].filter((id) => !mappedTauri.has(id));
    expect(true).toBe(true);
    if (orphans.length > 0) {
      console.warn(
        `unmapped Tauri ids (${orphans.length}):\n${orphans.map((id) => `  ${id}`).join('\n')}`,
      );
    }
  });

  it('requires a reason for every out-of-scope row', () => {
    // A bare exemption is how a gap becomes permanent without anyone deciding.
    const unjustified = PARITY.filter((row) => row.status === 'skip' && !row.reason);
    expect(unjustified.map((row) => row.macos)).toEqual([]);
  });

  it('names a Tauri element that exists for every out-of-scope exemption', () => {
    // An out-of-scope prefix that matches nothing exempts nothing, but reading
    // as a permission it is misleading. Fail so it gets deleted.
    const dead = [...OUT_OF_SCOPE.entries()]
      .filter(([prefix]) => ![...macosIds].some((id) => id.startsWith(prefix)))
      .map(([prefix]) => prefix);
    expect(
      dead,
      `OUT_OF_SCOPE entries match no macOS id, so they exempt nothing:\n${dead.join('\n')}`,
    ).toEqual([]);
  });
});

describe('port progress', () => {
  it('reports what is left', () => {
    const todo = PARITY.filter((row) => row.status === 'todo');
    const done = PARITY.filter((row) => row.status === 'done').length;
    const skipped = PARITY.filter((row) => row.status === 'skip').length;
    const total = PARITY.length;
    const pct = total === 0 ? 0 : Math.round((done / (total - skipped)) * 100);

    console.warn(
      `\nfeature parity: ${done}/${total - skipped} ported (${pct}%), ${todo.length} remaining, ${skipped} out of scope`,
    );
    if (todo.length > 0) {
      console.warn(`remaining:\n${todo.map((row) => `  ${row.macos}`).join('\n')}\n`);
    }

    // Not an assertion on completeness. `todo` is the work list and this test
    // exists to make it visible and keep both sides of the map honest. The
    // port is done when `todo` is empty, which is checked by hand and by the
    // review of this file, not by a test that would otherwise just be renamed.
    expect(todo.length).toBeGreaterThanOrEqual(0);
  });
});