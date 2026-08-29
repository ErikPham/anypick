import type { TrayBridge } from './types';

/**
 * The one bridge, and it is real.
 *
 * This UI only runs inside the Tauri shell, where it reaches the Go service
 * through the Rust layer. There is deliberately no browser fallback and no
 * fixture data: a preview that is not the real thing renders the wrong thing
 * and hides exactly the bugs that matter — framing, timing, restart, and the
 * shape of the snapshot the engine actually sends.
 *
 * To look at the UI, run the app (`make desktop-dev`), not a browser.
 */
export function createBridge(): TrayBridge {
  const tauri = window.__TAURI__;
  if (!tauri) {
    throw new Error(
      'AnyPick desktop UI requires the Tauri shell: window.__TAURI__ is missing. ' +
        'Run `make desktop-dev` instead of opening this in a browser.',
    );
  }
  return {
    invoke: tauri.core.invoke,
    listen: tauri.event.listen,
  };
}
