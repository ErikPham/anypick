/**
 * Automatic Codex profile creation, matching macOS `AppPreferences` and
 * `ProductionModel.synchronizeAutomaticCodexProfiles`.
 *
 * The preference stays on this machine. The engine has no
 * `setting-create-codex-profiles` operation, and sending one rejects the
 * frame while the switch stays on. New account sources are created with
 * `codex-profile-sync`, which is the operation the backfill button already uses.
 */

import type { Snapshot } from './contract';

const ENABLED_KEY = 'anypick.codexAutoProfiles';
const FINGERPRINTS_KEY = 'anypick.codexAutoProfileFingerprints';

/** Sources already attempted this session. A failure is not retried until the preference is set again. */
const attempted = new Set<string>();

/** One-way local fingerprint so the preference file does not store the source id. */
export function codexAutoProfileFingerprint(sourceId: string): string {
  let h1 = 0x811c9dc5;
  let h2 = 0x01000193;
  for (let i = 0; i < sourceId.length; i++) {
    const c = sourceId.charCodeAt(i);
    h1 ^= c;
    h1 = Math.imul(h1, 0x01000193);
    h2 ^= c;
    h2 = Math.imul(h2, 0x811c9dc5);
  }
  return (h1 >>> 0).toString(16).padStart(8, '0') + (h2 >>> 0).toString(16).padStart(8, '0');
}

export function codexAutoProfilesEnabled(): boolean {
  try {
    const raw = globalThis.localStorage?.getItem(ENABLED_KEY);
    if (raw === null || raw === undefined) return true;
    return raw === 'true';
  } catch {
    return true;
  }
}

/**
 * `setAutomaticCodexProfileCreation`. Enabling replaces the baseline with the
 * sources that already have profiles, so those are not created again. Missing
 * sources stay eligible for the next sync.
 */
export function setAutomaticCodexProfileCreation(enabled: boolean, baselineSourceIds: readonly string[]): void {
  try {
    globalThis.localStorage?.setItem(ENABLED_KEY, enabled ? 'true' : 'false');
    if (enabled) {
      const prints = baselineSourceIds.map(codexAutoProfileFingerprint);
      globalThis.localStorage?.setItem(FINGERPRINTS_KEY, JSON.stringify(prints));
    }
  } catch {
    // A preference that cannot be stored still applies for this session.
  }
  attempted.clear();
}

function readFingerprints(): Set<string> {
  try {
    const raw = globalThis.localStorage?.getItem(FINGERPRINTS_KEY);
    if (!raw) return new Set();
    const parsed = JSON.parse(raw) as unknown;
    if (!Array.isArray(parsed)) return new Set();
    return new Set(parsed.filter((id): id is string => typeof id === 'string'));
  } catch {
    return new Set();
  }
}

function writeFingerprints(known: Set<string>): void {
  try {
    globalThis.localStorage?.setItem(FINGERPRINTS_KEY, JSON.stringify([...known]));
  } catch {
    // The in-memory baseline still applies for this session.
  }
}

/**
 * Account sources that should be created now. Empty when the preference is
 * off, a mutation is in flight, or every candidate was already attempted.
 * The returned batch is marked attempted, so a later snapshot does not send it again.
 */
export function planAutomaticCodexProfileSync(snapshot: Snapshot, busy: boolean): string[] {
  if (!codexAutoProfilesEnabled() || busy) return [];
  const accountSourceIDs = snapshot.codexProfileSources
    .filter((source) => source.kind === 'account')
    .map((source) => source.sourceId);
  if (accountSourceIDs.length === 0) return [];
  const profilesBySource = new Set(snapshot.codexProfiles.map((profile) => profile.sourceId));
  const known = readFingerprints();
  let changed = false;
  for (const sourceID of accountSourceIDs) {
    if (!profilesBySource.has(sourceID)) continue;
    const fingerprint = codexAutoProfileFingerprint(sourceID);
    if (!known.has(fingerprint)) {
      known.add(fingerprint);
      changed = true;
    }
  }
  if (changed) writeFingerprints(known);
  const candidates = accountSourceIDs.filter((sourceID) => {
    const fingerprint = codexAutoProfileFingerprint(sourceID);
    return !known.has(fingerprint) && !attempted.has(fingerprint) && !profilesBySource.has(sourceID);
  });
  const batch = candidates.slice(0, 64);
  for (const sourceID of batch) attempted.add(codexAutoProfileFingerprint(sourceID));
  return batch;
}
