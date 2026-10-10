import { afterEach, describe, expect, it } from 'vitest';

import type { Snapshot } from '../lib/contract';
import {
  codexAutoProfilesEnabled,
  planAutomaticCodexProfileSync,
  setAutomaticCodexProfileCreation,
} from '../lib/codex-auto-profiles';
import { snapshotFixture } from './harness';

const ENABLED_KEY = 'anypick.codexAutoProfiles';
const FINGERPRINTS_KEY = 'anypick.codexAutoProfileFingerprints';

function snapshotWith(sources: Snapshot['codexProfileSources'], profiles: Snapshot['codexProfiles'] = []): Snapshot {
  return snapshotFixture({ codexProfileSources: sources, codexProfiles: profiles });
}

afterEach(() => {
  setAutomaticCodexProfileCreation(false, []);
  localStorage.removeItem(ENABLED_KEY);
  localStorage.removeItem(FINGERPRINTS_KEY);
});

describe('automatic codex profiles', () => {
  it('defaults on and does not invent an engine setting', () => {
    localStorage.removeItem(ENABLED_KEY);
    expect(codexAutoProfilesEnabled()).toBe(true);
  });

  it('creates a new account source once, and skips sources that already have profiles', () => {
    const snapshot = snapshotWith(
      [
        { sourceId: 'account/codex/work', kind: 'account', label: 'Work', detail: '', selected: true, models: [] },
        { sourceId: 'account/codex/personal', kind: 'account', label: 'Personal', detail: '', selected: false, models: [] },
      ],
      [
        {
          name: 'work',
          sourceId: 'account/codex/work',
          sourceLabel: 'Work',
          modelId: '',
          transportPolicy: 'proxy',
          launchCommand: '',
        },
      ],
    );
    expect(planAutomaticCodexProfileSync(snapshot, false)).toEqual(['account/codex/personal']);
    expect(planAutomaticCodexProfileSync(snapshot, false)).toEqual([]);
  });

  it('waits for a non-Codex model catalog before creating its profile', () => {
    const unavailable = snapshotWith([
      { sourceId: 'account/zai/work', kind: 'account', label: 'Work', detail: '', selected: true, models: [] },
    ]);
    expect(planAutomaticCodexProfileSync(unavailable, false)).toEqual([]);

    const available = snapshotWith([
      {
        sourceId: 'account/zai/work',
        kind: 'account',
        label: 'Work',
        detail: '',
        selected: true,
        models: ['glm-5'],
      },
    ]);
    expect(planAutomaticCodexProfileSync(available, false)).toEqual(['account/zai/work']);
  });

  it('baselines existing profiles when turned on, then creates only the new ones', () => {
    setAutomaticCodexProfileCreation(false, []);
    const before = snapshotWith([
      { sourceId: 'account/codex/work', kind: 'account', label: 'Work', detail: '', selected: true, models: [] },
    ]);
    expect(planAutomaticCodexProfileSync(before, false)).toEqual([]);
    setAutomaticCodexProfileCreation(true, ['account/codex/work']);
    const after = snapshotWith(
      [
        { sourceId: 'account/codex/work', kind: 'account', label: 'Work', detail: '', selected: true, models: [] },
        { sourceId: 'account/codex/personal', kind: 'account', label: 'Personal', detail: '', selected: false, models: [] },
      ],
      [
        {
          name: 'work',
          sourceId: 'account/codex/work',
          sourceLabel: 'Work',
          modelId: '',
          transportPolicy: 'proxy',
          launchCommand: '',
        },
      ],
    );
    expect(planAutomaticCodexProfileSync(after, false)).toEqual(['account/codex/personal']);
  });

  it('stays quiet while a mutation is in flight', () => {
    const snapshot = snapshotWith([
      { sourceId: 'account/codex/personal', kind: 'account', label: 'Personal', detail: '', selected: false, models: [] },
    ]);
    expect(planAutomaticCodexProfileSync(snapshot, true)).toEqual([]);
    expect(planAutomaticCodexProfileSync(snapshot, false)).toEqual(['account/codex/personal']);
  });
});
