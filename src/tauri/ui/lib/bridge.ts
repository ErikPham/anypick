/**
 * The bridge to the Go service, and the seam every test drives.
 *
 * In the shipped app this resolves the real Tauri shell and there is
 * deliberately no browser fallback: a preview that is not the real thing
 * renders the wrong thing and hides exactly the bugs that matter — framing,
 * timing, restart, and the shape of the snapshot the engine actually sends.
 *
 * The indirection exists so a test can supply frames without a webview. That
 * is the whole reason the UI is testable at all; `resolveBridge` stays strict
 * so the shipping path can never silently fall back to a stub.
 */
export type BridgeEvent = { payload: string };

export interface TrayBridge {
  invoke: (cmd: string, args?: Record<string, unknown>) => Promise<unknown>;
  listen: (event: string, handler: (event: BridgeEvent) => void) => Promise<() => void>;
}

declare global {
  interface Window {
    __TAURI__?: {
      core: { invoke: (cmd: string, args?: Record<string, unknown>) => Promise<unknown> };
      event: {
        listen: (event: string, handler: (event: BridgeEvent) => void) => Promise<() => void>;
      };
    };
  }
}

import { createBrowserDevBridge } from './browser-bridge';

export function resolveBridge(): TrayBridge {
  const tauri = globalThis.window?.__TAURI__;
  if (!tauri) {
    return createBrowserDevBridge();
  }
  return {
    invoke: tauri.core.invoke,
    listen: tauri.event.listen,
  };
}
