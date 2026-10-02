/**
 * Reads the feature inventory out of both source trees.
 *
 * The parity test and `make check-parity` share this so the check and the
 * report can never disagree about what the inventory is. Extraction is the
 * part that quietly rots, so it lives in one place with its own guard.
 */

import { readdirSync, readFileSync } from 'node:fs';
import { join, resolve } from 'node:path';

const macosDir = resolve(__dirname, '../../../macos');
const uiDir = resolve(__dirname, '..');

/** Every source file that can name a Tauri element. */
function uiSources(dir: string): string[] {
  const files: string[] = [];
  const visit = (current: string) => {
    for (const entry of readdirSync(current, { withFileTypes: true })) {
      if (entry.name === 'node_modules' || entry.name === 'test') continue;
      const path = join(current, entry.name);
      if (entry.isDirectory()) visit(path);
      else if (/\.(tsx|ts)$/.test(entry.name)) files.push(path);
    }
  };
  visit(dir);
  return files;
}

/**
 * Every accessibility identifier the macOS app publishes.
 *
 * Three shapes reach `.accessibilityIdentifier`, and a scan that reads only the
 * first one under-reports the feature inventory:
 *
 *  1. a literal argument: `.accessibilityIdentifier("hub.rotate-api-key")`
 *  2. a component parameter: `DesignCopyButton(identifier: "hub.copy-address")`
 *     applies it itself (`SharedUI.swift:301`), so the literal is not next to
 *     the word `accessibilityIdentifier` at the call site
 *  3. a computed helper: `strategyIdentifier(for:)` returns a literal, so the id
 *     exists only as a return value
 *
 * Swift interpolates with `\(expr)`, which can itself contain parentheses
 * (`\(strategyIdentifier(for: rotation))`), so `normalizeInterpolation` counts
 * nesting rather than stopping at the first `)`.
 */
export function macosIdentifiers(): string[] {
  const ids = new Set<string>();
  for (const file of readdirSync(macosDir)) {
    if (!file.endsWith('.swift')) continue;
    const source = readFileSync(join(macosDir, file), 'utf8');

    for (const match of source.matchAll(/accessibilityIdentifier\(\s*"((?:[^"\\]|\\.)*)"\s*\)/g)) {
      ids.add(normalizeInterpolation(match[1]));
    }
    // Shape 2. A component parameter. Two spellings reach the tree: a plain
    // `identifier:`, which `DesignCopyButton` forwards itself
    // (`SharedUI.swift:301`), and `accessibilityIdentifier:`, which is a
    // labelled argument on `DesignCandidatePicker`. Reading only the first
    // missed `hub.conflict.<id>.picker` and five published-model rows; reading
    // only the second missed the copy buttons.
    for (const match of source.matchAll(/(?<![A-Za-z])identifier:\s*"([^"]+)"/g)) {
      ids.add(normalizeInterpolation(match[1]));
    }
    for (const match of source.matchAll(/accessibilityIdentifier:\s*"([^"]+)"/g)) {
      ids.add(normalizeInterpolation(match[1]));
    }
    // Shape 3. A computed id that reaches the tree through a variable:
    //   `let rowKey = cond ? "hub.rotation.\(id)" : "hub.account.\(id)"`
    //   `.accessibilityIdentifier(rowKey)`
    // Every literal in such a binding is a distinct id, and the variable's own
    // use site carries no literal to find. `ProxyHubView.swift:451,500` is the
    // case; without this the two hub source-row ids were absent entirely.
    for (const match of source.matchAll(
      /\blet\s+(\w*[Ii]d\w*|\w*[Kk]ey)\s*(?::\s*String\s*)?=\s*([^;\n]{0,200})/g,
    )) {
      const initializer = match[2];
      // `cond ? "a" : "b"` is one id per arm. A single literal is one id. A
      // call or a concatenation is a format, not an id, and its values belong
      // to the other shapes.
      const ternary = /^[^?"]*\?\s*"[^"]*"\s*:\s*"[^"]*"\s*$/.test(initializer.trim());
      const single = /^"[^"]*"$/.test(initializer.trim());
      if (!ternary && !single) continue;
      for (const literal of initializer.matchAll(/"([^"]+)"/g)) {
        ids.add(normalizeInterpolation(literal[1]));
      }
    }

    // Shape 4. A helper function that returns a literal id.
    for (const match of source.matchAll(
      /func\s+\w*[Ii]dentifier\w*\s*\([^)]*\)\s*(?:->\s*String\s*)?\{([^}]{0,400})\}/g,
    )) {
      for (const literal of match[1].matchAll(/"([^"]+)"/g)) {
        ids.add(normalizeInterpolation(literal[1]));
      }
    }
  }
  return [...ids].sort();
}

/** `\(anything, parens included)` becomes `{x}`, so it compares to a template id. */
function normalizeInterpolation(raw: string): string {
  let out = '';
  for (let i = 0; i < raw.length; i += 1) {
    if (raw[i] === '\\' && raw[i + 1] === '(') {
      let depth = 1;
      i += 2;
      while (i < raw.length && depth > 0) {
        if (raw[i] === '(') depth += 1;
        else if (raw[i] === ')') depth -= 1;
        i += 1;
      }
      i -= 1;
      out += '{x}';
      continue;
    }
    out += raw[i];
  }
  return out;
}

/**
 * Every `data-testid` and `testId` the UI emits.
 *
 * Both spellings are read because a pane passes `testId` down through a
 * primitive rather than always writing the attribute itself. Only literal and
 * template-literal values count: a bare `{testId}` is a prop being forwarded
 * and names nothing.
 */
export function tauriIdentifiers(): string[] {
  const ids = new Set<string>();
  for (const path of uiSources(uiDir)) {
    const source = readFileSync(path, 'utf8');
    const pattern = /(?:data-testid|testId)\s*=\s*(?:\{\s*)?(?:"([^"]+)"|`([^`]+)`)/g;
    for (const match of source.matchAll(pattern)) {
      const value = match[1] ?? match[2] ?? '';
      if (!value || value === 'testId') continue;
      ids.add(value);
    }
  }
  return [...ids].sort();
}

/** `${action.id}` normalizes to `{x}`, matching the Swift side. */
export function normalizeTauri(value: string): string {
  return value.replace(/\$\{[^}]*\}/g, '{x}');
}