import type { BindingStore } from './binding-store';

/**
 * Keep the native client for a provider pointed at the login `accounts.use`
 * just made live.
 *
 * Switching Gemini/Codex from Accounts (or the TUI account list) only restores
 * files + the provider active pointer. The homonymous client binding is a
 * separate record, and leaving it stale is what the tray reports as
 * "bound to A, but B is signed in."
 *
 * Only the client whose id equals the provider is updated. A Claude route
 * through `gemini/work` is a proxy source and must keep that snapshot.
 */
export function realignNativeClientBinding(
  bindings: BindingStore,
  providerId: string,
  accountName: string,
): boolean {
  const binding = bindings.getGlobal(providerId);
  if (!binding) {
    return false;
  }
  const source = binding.spec.source;
  if (source.kind !== 'account' || source.provider !== providerId || source.name === accountName) {
    return false;
  }
  bindings.upsertGlobal(
    providerId,
    { ...binding.spec, source: { ...source, name: accountName } },
    binding.provenance,
    binding.managedConfigRevision,
  );
  return true;
}
