/// <reference types="vite/client" />

/** tauri.conf.json `version`, injected by scripts/app-version.ts. */
declare const __ANYPICK_VERSION__: string;

declare module '*.css' {
  const css: string;
  export default css;
}

declare module '*.svg' {
  const src: string;
  export default src;
}
