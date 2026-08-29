import type { AnyPick } from './core/app';
import type { AnyPickEventSink } from './core/events';
import type { BackendApi } from './core/backend-api';

/** Options accepted by the supported programmatic application factory. */
export interface CreateAnyPickAppOptions {
  /** Data root. Defaults to `~/.anypick` (or `ANYPICK_HOME`). */
  root?: string;
  /** Optional sanitized lifecycle-event sink owned by the caller. */
  events?: AnyPickEventSink;
}

/**
 * Open a fully initialized AnyPick application.
 *
 * The promise resolves only after schema migration, legacy migration, plugin
 * loading, and startup recovery have completed. The returned facade excludes
 * SQLite, stores, journals, and leases; call `close()` when finished.
 */
export async function createAnyPickApp(options: CreateAnyPickAppOptions = {}): Promise<AnyPick> {
  const { createAppReady } = await import('./core/app');
  return createAppReady(options);
}

/**
 * Create the versioned, secret-free UI boundary. SwiftUI/Tauri callers should
 * use this rather than reading AnyPick's SQLite, credential files, or stores.
 */
export async function createAnyPickBackend(
  options: CreateAnyPickAppOptions = {},
): Promise<BackendApi & { close(): void }> {
  const [{ createAppReady }, { createBackendApi }] = await Promise.all([
    import('./core/app'),
    import('./core/backend-api'),
  ]);
  const app = await createAppReady(options);
  const backend = createBackendApi(app);
  return {
    ...backend,
    close(): void {
      backend.close();
      app.close();
    },
  };
}

export type { AnyPick } from './core/app';
export type { BackendApi } from './core/backend-api';
export type { BackendCommand } from './types/backend';
