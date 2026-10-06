/** The Tauri app version from tauri.conf.json, fixed at build time. */
export const APP_VERSION: string = __ANYPICK_VERSION__;

/** The one-line About footer in Settings, e.g. "AnyPick 1.1.0". */
export function aboutLine(version: string = APP_VERSION): string {
  return `AnyPick ${version}`;
}
