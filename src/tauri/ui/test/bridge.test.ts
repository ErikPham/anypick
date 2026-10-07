import { afterEach, describe, expect, it } from 'vitest';

import { resolveBridge } from '../lib/bridge';

/**
 * `resolveBridge` is the shipping path, not a convenience.
 *
 * The bundle once carried a fixture fallback that rendered fabricated accounts
 * to anyone whose window was not hosted by Tauri. These cases pin the strict
 * behaviour the doc comment above the function promises.
 */
describe('resolveBridge', () => {
  afterEach(() => {
    delete (globalThis.window as unknown as Record<string, unknown>).__TAURI__;
  });

  it('throws when window.__TAURI__ is absent instead of falling back to a fixture', () => {
    delete (globalThis.window as unknown as Record<string, unknown>).__TAURI__;

    expect(() => resolveBridge()).toThrow(/__TAURI__/);
  });

  it('throws when window.__TAURI__ is present but falsy', () => {
    // A shim that assigns `null` is not a shell either. The check is on the
    // value, not on the property existing.
    (globalThis.window as unknown as Record<string, unknown>).__TAURI__ = null;

    expect(() => resolveBridge()).toThrow(/__TAURI__/);
  });

  it('resolves the real invoke and listen when the shell is present', async () => {
    const invoke = async () => 'ok';
    const listen = async () => () => {};
    (globalThis.window as unknown as Record<string, unknown>).__TAURI__ = {
      core: { invoke },
      event: { listen },
    };

    const bridge = resolveBridge();
    await expect(bridge.invoke('service_status')).resolves.toBe('ok');
    await expect(bridge.listen('service-line', () => {})).resolves.toBeTypeOf('function');
  });
});
