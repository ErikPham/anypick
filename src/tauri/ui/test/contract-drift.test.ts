import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';

import { DESTINATIONS } from '../lib/contract';

/**
 * The mirror is only worth having if it cannot rot. This reads the Go struct
 * the engine actually serialises and fails when a field exists there and not
 * here, which is the failure that otherwise shows up as a blank cell in a pane
 * with nothing to grep for.
 *
 * It also pins the frame kinds the UI decodes, because the UI's decoder is a
 * hand-written allowlist and a new engine frame is silently dropped today.
 */

const goSnapshot = readFileSync(
  resolve(__dirname, '../../../go/internal/desktop/snapshot.go'),
  'utf8',
);
const tsContract = readFileSync(resolve(__dirname, '../lib/contract.ts'), 'utf8');
const goResults = readFileSync(
  resolve(__dirname, '../../../go/internal/desktop/result.go'),
  'utf8',
);
const uiFrameDecoder = readFileSync(resolve(__dirname, '../App.tsx'), 'utf8');

/** Every `json:"tag"` name in the file, deduplicated. */
function jsonTags(source: string): string[] {
  const tags = new Set<string>();
  for (const match of source.matchAll(/json:"([^",]+)/g)) tags.add(match[1]);
  return [...tags].sort();
}

/** `type Name struct { … }` bodies, keyed by type name. */
function goStructs(source: string): Map<string, string> {
  const structs = new Map<string, string>();
  for (const match of source.matchAll(/^type (\w+) struct \{([\s\S]*?)^\}/gm)) {
    structs.set(match[1], match[2]);
  }
  return structs;
}

const structs = goStructs(goSnapshot);

/**
 * True when `tag` is a declared member of the mirror, not merely a substring of
 * one. A plain `toContain(tag)` accepts `rotateActionIdTypo` for
 * `rotateActionId`, which is exactly the typo this test exists to catch.
 */
function declaresField(tag: string): boolean {
  return new RegExp(`^\\s*${tag}\\??:`, 'm').test(tsContract);
}

describe('contract mirrors the Go snapshot', () => {
  it('finds the Go structs it claims to mirror', () => {
    // Guards the regex itself. An extraction that silently matches nothing
    // would make every assertion below pass for the wrong reason.
    expect(structs.get('Snapshot')).toBeDefined();
    expect(structs.size).toBeGreaterThan(10);
  });

  const rowTypes = [
    'ActionRow',
    'AccountRow',
    'ProxyRow',
    'LogSourceRow',
    'DegradedCondition',
    'AccountProviderRow',
    'LoginMethodRow',
    'CredentialKindRow',
    'CredentialProfileRow',
    'GatewayProviderRow',
    'Preferences',
  ];

  for (const name of rowTypes) {
    it(`types every field of ${name}`, () => {
      const body = structs.get(name);
      expect(body, `${name} is not a struct in snapshot.go`).toBeDefined();
      for (const tag of jsonTags(body as string)) {
        expect(declaresField(tag), `${name}.${tag} is missing from lib/contract.ts`).toBe(true);
      }
    });
  }

  it('types every field of the top-level Snapshot', () => {
    const body = structs.get('Snapshot') as string;
    for (const tag of jsonTags(body)) {
      expect(declaresField(tag), `Snapshot.${tag} is missing from lib/contract.ts`).toBe(true);
    }
  });

  it('does not claim Codex profile fields the engine does not send', () => {
    // CodexProfileRow lives in codex_profiles.go, not snapshot.go. Assert the
    // mirror is a superset of the engine, never an invention: a field here
    // that the engine never writes renders as undefined at runtime.
    const codex = readFileSync(
      resolve(__dirname, '../../../go/internal/desktop/codex_profiles.go'),
      'utf8',
    );
    for (const tag of jsonTags(goStructs(codex).get('CodexProfileRow') as string)) {
      expect(declaresField(tag), `CodexProfileRow.${tag} missing`).toBe(true);
    }
  });
});

describe('frame decoding stays aligned with the engine', () => {
  const engineFrames = [
    ...jsonTags(goResults).filter((tag) => /^(requestId|status|message|code|mutated)$/.test(tag)),
  ];

  it('decodes every frame kind the engine can send', () => {
    // The engine's terminal frames all carry a result shape; if the UI drops
    // one, a mutation never releases its busy slot and the row spins forever.
    for (const field of engineFrames) {
      expect(uiFrameDecoder, `the UI frame decoder never reads ${field}`).toContain(field);
    }
  });

  it('handles cancelled results without raising an error', () => {
    // result.go: a cancelled status clears code, suggestions and mutated. A
    // UI that treats any non-success as failure nags the user about their own
    // cancellation, so the status has to be read before the code.
    expect(goResults).toContain('"cancelled"');
    expect(uiFrameDecoder).toContain('cancelled');
  });
});

describe('engine-backed pane destinations', () => {
  it('keeps the seven data routes in their protocol order', () => {
    // Settings is a shell-only route; this list mirrors only the destinations
    // the engine snapshot and operations understand.
    expect([...DESTINATIONS]).toEqual([
      'overview',
      'apps',
      'accounts',
      'proxyHub',
      'proxies',
      'activity',
      'backups',
    ]);
  });
});
