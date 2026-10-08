import { describe, expect, it, vi } from 'vitest';

import type { BridgeEvent, TrayBridge } from '../lib/bridge';
import { createService } from '../lib/service';

/**
 * A bridge whose `send_command` rejects, the way the Tauri host answers when
 * the sidecar has stopped.
 *
 * Rust returns `Err("AnyPick core is stopped. Restart the app to try again.")`
 * from `send_command`, so the invoke promise rejects rather than resolving.
 * Nothing here listens, because these cases are about a command that never
 * reaches the engine.
 */
class RejectingBridge implements TrayBridge {
  readonly invoked: string[] = [];
  constructor(private readonly message = 'AnyPick core is stopped. Restart the app to try again.') {}

  async invoke(cmd: string, _payload?: unknown): Promise<unknown> {
    this.invoked.push(cmd);
    if (cmd === 'send_command') throw new Error(this.message);
    return undefined;
  }

  async listen(_event: string, _handler: (event: BridgeEvent) => void): Promise<() => void> {
    return () => {};
  }
}

describe('a command the host refuses', () => {
  it('reports refresh failures instead of dropping them', async () => {
    const bridge = new RejectingBridge();
    const onError = vi.fn();
    const service = createService({ bridge, onError });

    service.port.refresh();
    await vi.waitFor(() => expect(onError).toHaveBeenCalledTimes(1));

    expect(onError).toHaveBeenCalledWith('AnyPick core is stopped. Restart the app to try again.');
  });

  it('reports cancel failures and settles the id it was cancelling', async () => {
    // The real shape: the login mutate SUCCEEDED on the live bridge (the id is
    // genuinely in flight, a sign-in is running), and only the cancel command
    // is refused — the sidecar stopped between the two. A bridge that rejects
    // everything cannot see the leak, because mutate's own catch settles the
    // id first and masks it.
    let cancelAttempts = 0;
    const bridge = new RejectingBridge();
    const realInvoke = bridge.invoke.bind(bridge);
    bridge.invoke = async (cmd: string, payload?: unknown) => {
      const inner = (payload as { command?: string } | undefined)?.command ?? '';
      if (inner.startsWith('cancel')) {
        cancelAttempts += 1;
        return realInvoke(cmd); // reject only the cancel
      }
      return { ok: true };
    };
    const onError = vi.fn();
    const service = createService({ bridge, onError });

    const requestId = service.port.mutate('account-login', {});
    expect(service.pending().has(requestId)).toBe(true);
    expect(onError).not.toHaveBeenCalled();

    service.port.cancel(requestId);
    expect(cancelAttempts).toBe(1);
    await vi.waitFor(() => expect(onError).toHaveBeenCalledTimes(1));
    expect(onError).toHaveBeenCalledWith('AnyPick core is stopped. Restart the app to try again.');

    // The engine never hears the cancelled command either, so no result frame
    // will settle it. A cancel catch that only reports leaks the id forever:
    // busy.size stays above zero, controls stay disabled, and the shell effect
    // never runs again.
    expect(service.pending().has(requestId)).toBe(false);
  });

  it('reports a failed mutation and releases the control it held', async () => {
    const bridge = new RejectingBridge();
    const onError = vi.fn();
    const service = createService({ bridge, onError });

    const requestId = service.port.mutate('account-backup-all', { path: '' });
    expect(service.pending().has(requestId)).toBe(true);
    await vi.waitFor(() => expect(onError).toHaveBeenCalledTimes(1));

    // The engine never hears about a command the host refused, so no result
    // frame will ever settle it. Settling here is what stops a row spinning.
    expect(service.pending().has(requestId)).toBe(false);
  });
});
