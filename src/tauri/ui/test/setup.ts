import '@testing-library/jest-dom/vitest';

// The UI renders inside a Tauri webview, so nothing in jsdom provides the
// host surface a pane can legitimately reach for: the clipboard write, the
// confirm dialog behind the destructive Stop All, and the matchMedia probe
// behind the appearance watcher.
//
// userEvent.setup() replaces navigator.clipboard with its own stub. A test
// that wants the text the app copied reads navigator.clipboard.readText()
// after the write, not a log captured on this object.
Object.defineProperty(globalThis.navigator, 'clipboard', {
  configurable: true,
  value: {
    writeText: () => Promise.resolve(),
    readText: () => Promise.resolve(''),
  },
});

if (!('confirm' in globalThis) || !globalThis.confirm) {
  Object.defineProperty(globalThis, 'confirm', {
    configurable: true,
    value: () => true,
    writable: true,
  });
}

if (!globalThis.matchMedia) {
  Object.defineProperty(globalThis, 'matchMedia', {
    configurable: true,
    value: (query: string) => ({
      matches: false,
      media: query,
      onchange: null,
      addEventListener: () => {},
      removeEventListener: () => {},
      addListener: () => {},
      removeListener: () => {},
      dispatchEvent: () => false,
    }),
  });
}

if (!globalThis.localStorage || typeof globalThis.localStorage.getItem !== 'function') {
  const store = new Map<string, string>();
  const localStorageMock: Storage = {
    getItem: (key: string) => store.get(key) ?? null,
    setItem: (key: string, value: string) => {
      store.set(key, String(value));
    },
    removeItem: (key: string) => {
      store.delete(key);
    },
    clear: () => {
      store.clear();
    },
    key: (index: number) => Array.from(store.keys())[index] ?? null,
    get length() {
      return store.size;
    },
  };
  Object.defineProperty(globalThis, 'localStorage', {
    configurable: true,
    value: localStorageMock,
    writable: true,
  });
}
