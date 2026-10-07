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

  async invoke(cmd: string): Promise<unknown> {
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

  it('reports cancel failures instead of dropping them', async () => {
    const bridge = new RejectingBridge();
    const onError = vi.fn();
    const service = createService({ bridge, onError });

    service.port.cancel('req-1');
    await vi.waitFor(() => expect(onError).toHaveBeenCalledTimes(1));

    expect(onError).toHaveBeenCalledWith('AnyPick core is stopped. Restart the app to try again.');
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
