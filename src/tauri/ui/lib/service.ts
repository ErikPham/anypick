import type { TrayBridge } from './bridge';
import type { ZcodeContext } from './account-flow';
import {
  backupInspectCommand,
  cancelCommand,
  doctorCommand,
  invokeCommand,
  logsCommand,
  modelRolesCommand,
  mutateCommand,
  newRequestId,
  type MutateExtra,
} from './wire';

/**
 * The one place a pane asks the engine to do something.
 *
 * A pane never builds a wire line and never mints a request id. It calls one of
 * these methods, which mints the id, marks the call in flight, and hands the
 * engine a revision-scoped command. Keeping that here is what lets the shell
 * release a control on the engine's answer rather than on a timer: the id
 * returned by `begin` is the id the `result` frame will carry back.
 */
export interface ServicePort {
  /**
   * Apply a published action. `revision` must be the revision the action id
   * came from: ids are revision-scoped and a stale one is refused.
   */
  invokeAction(actionId: string, revision: number, allowForeignConfig?: boolean): void;
  /**
   * Run one mutation. `extra` carries only the keys the operation documents.
   * The returned id is the one a later `cancel` must name.
   */
  mutate(operation: string, extra?: MutateExtra): string;
  /** Choose models by role. The engine requires a complete map, not one role. */
  /** Returns the request id, so the sheet can wait for this command's answer. */
  setModelRoles(
    clientId: string,
    roleActionIds: Record<string, string>,
    revision: number,
    scope?: 'connection' | 'provider',
  ): string;
  /** Ask for the tail of one proxy log. `lines` is capped at the engine's 200. */
  fetchLogs(providerId: string, name: string, lines?: number): string;
  /** Stage a backup file for review. The preview arrives as its own frame. */
  inspectBackup(path: string): string;
  /** Ask the engine to check the installation. */
  requestDoctor(): string;
  /** Cancel an in-flight command by the id `mutate` returned. */
  cancel(requestId: string): void;
  /** Ask for a fresh snapshot. */
  refresh(): void;
  /**
   * The commands the engine still owes an answer for, without their request ids.
   *
   * `busy.size > 0` says *something* is in flight. macOS says which service:
   * `serviceOperations[proxy.id]` is what puts "Stopping Work" beside a spinner
   * *inside that row's own button* rather than a bare spinner, and HIG is
   * explicit that the button must stay and spin inside it. Reading the flight's
   * command is how a pane tells its own row from another's.
   */
  inFlight(): readonly InFlight[];
  /** Copy to the host clipboard. A host refusal is not a pane failure. */
  copy(text: string): Promise<boolean>;
  /**
   * Write pasted auth JSON to a private temp file. The mutate line carries
   * the path, never the document. The engine's command cap is 16 KiB.
   */
  /**
   * What the ZCode desktop app left on this Mac: whether it is installed, and
   * whether its plan cache reports a connected plan. Plan-status context only —
   * the provider owns reading the encrypted login.
   */
  zcodeContext(): Promise<ZcodeContext | null>;
  stageAuthPaste(body: string): Promise<string>;
  /** Delete a file `stageAuthPaste` returned. A path outside that directory is refused. */
  forgetAuthPaste(path: string): Promise<void>;
}

/** What one outstanding command was, so the answer can be applied to that command. */
export type InFlight =
  | { kind: 'invoke'; actionId: string }
  | { kind: 'mutate'; operation: string }
  /**
   * `model-roles`. Registered so the result frame can be told apart from a
   * snapshot refresh: without it a failed pin change had no owner, so the sheet
   * closed on a command the engine had refused and nothing said why.
   */
  | { kind: 'model-roles'; clientId: string }
  | { kind: 'logs' }
  | { kind: 'backup-inspect' }
  | { kind: 'doctor' };

export interface ServiceHandle {
  port: ServicePort;
  /** Request ids the engine still owes an answer for. */
  pending: () => ReadonlySet<string>;
  /**
   * The command behind a request id. Read it before `settle`: settling drops
   * the record, and a result with no command cannot be told apart from a
   * snapshot refresh.
   */
  lookup: (requestId: string) => InFlight | undefined;
  /** Release a request id once the engine has answered it. */
  settle: (requestId: string) => void;
}

/**
 * Build a port bound to one bridge and one busy-set.
 *
 * `onStart` is called with the new request id so the caller can disable the
 * control that produced it; `onSettle` is called with the id from the engine's
 * terminal frame, which is the only reliable moment to re-enable it.
 */
export function createService(options: {
  bridge: TrayBridge;
  /**
   * Called whenever the set of outstanding requests changes, so the shell can
   * disable controls while a mutation runs. The engine accepts one mutation at
   * a time, which is what makes this single-flight rather than advisory.
   */
  onBusyChange?: (pending: ReadonlySet<string>) => void;
  onError?: (message: string) => void;
}): ServiceHandle {
  const inFlight = new Set<string>();
  const flights = new Map<string, InFlight>();

  const settle = (requestId: string) => {
    flights.delete(requestId);
    if (!inFlight.delete(requestId)) return;
    options.onBusyChange?.(new Set(inFlight));
  };

  const send = (command: string, requestId: string) => {
    inFlight.add(requestId);
    options.onBusyChange?.(new Set(inFlight));
    void options.bridge
      .invoke('send_command', { command })
      .catch((error: unknown) => {
        // A command the host refused never reaches the engine, so nothing will
        // ever release it. Settling here is what stops a row spinning forever.
        settle(requestId);
        options.onError?.(error instanceof Error ? error.message : String(error));
      });
  };

  const inFlightCommands = () =>
    [...inFlight]
      .map((requestId) => flights.get(requestId))
      .filter((flight): flight is InFlight => Boolean(flight));

  const port: ServicePort = {
    inFlight: inFlightCommands,
    invokeAction(actionId, revision, allowForeignConfig = false) {
      if (!actionId) return;
      // One id for the command and for the in-flight slot. Minting a second
      // one here would track a request the engine never hears about, and the
      // control would stay disabled until the next unrelated answer arrived.
      const requestId = newRequestId('invoke');
      flights.set(requestId, { kind: 'invoke', actionId });
      send(invokeCommand(actionId, revision, requestId, allowForeignConfig), requestId);
    },
    mutate(operation, extra = {}) {
      const requestId = newRequestId(`mutate-${operation}`);
      flights.set(requestId, { kind: 'mutate', operation });
      send(mutateCommand(operation, extra, requestId), requestId);
      return requestId;
    },
    setModelRoles(clientId, roleActionIds, revision, scope) {
      if (!clientId) return '';
      // An empty map clears the pins. Any other map still has to name a
      // default, or the engine rejects the whole command.
      if (Object.keys(roleActionIds).length > 0 && !roleActionIds.default) return '';
      const requestId = newRequestId('model-roles');
      flights.set(requestId, { kind: 'model-roles', clientId });
      send(modelRolesCommand(clientId, roleActionIds, revision, requestId, scope), requestId);
      return requestId;
    },
    fetchLogs(providerId, name, lines = 200) {
      if (!providerId || !name) return '';
      const requestId = newRequestId('logs');
      flights.set(requestId, { kind: 'logs' });
      send(logsCommand(providerId, name, Math.min(Math.max(lines, 1), 200), requestId), requestId);
      return requestId;
    },
    inspectBackup(path) {
      if (!path) return '';
      const requestId = newRequestId('backup-inspect');
      flights.set(requestId, { kind: 'backup-inspect' });
      send(backupInspectCommand(path, requestId), requestId);
      return requestId;
    },
    requestDoctor() {
      const requestId = newRequestId('doctor');
      flights.set(requestId, { kind: 'doctor' });
      send(doctorCommand(requestId), requestId);
      return requestId;
    },
    cancel(requestId) {
      if (!requestId) return;
      // A refused cancel has no request id to settle and no frame coming back,
      // so without this the rejection is unhandled and the row the user just
      // cancelled spins on a command the engine never heard.
      void options.bridge.invoke('send_command', { command: cancelCommand(requestId) }).catch((error: unknown) => {
        // The id was seeded by mutate and lives in inFlight; a rejected cancel
        // means no result frame will ever arrive for it, so settle here — the
        // same contract send's catch already honours. Skipping this left
        // busy.size pinned above zero and the shell effect permanently blocked.
        settle(requestId);
        options.onError?.(error instanceof Error ? error.message : String(error));
      });
    },
    refresh() {
      // Try Again and the toolbar refresh both go through here. The sidecar
      // stopped is exactly when a user reaches for them, so an unhandled
      // rejection leaves the window on "Connecting to the service" with no way
      // out. The error banner is the way out.
      void options.bridge.invoke('send_command', { command: 'refresh' }).catch((error: unknown) => {
        options.onError?.(error instanceof Error ? error.message : String(error));
      });
    },
    async copy(text) {
      // Reports whether the text reached the clipboard, so a caller can show a
      // confirmation that is true. Returning void forced every call site to
      // either claim success on a refused write or say nothing at all, and
      // eight of them said nothing — including a key the user cannot select
      // and read back.
      if (!text) return false;
      try {
        await navigator.clipboard.writeText(text);
        return true;
      } catch {
        // A clipboard the host refuses is not a pane failure: every address in
        // this app is also selectable text, so there is nothing to interrupt
        // the user with. The caller decides what to say about it.
        return false;
      }
    },
    async zcodeContext() {
      const raw = await options.bridge.invoke('zcode_context');
      if (!raw || typeof raw !== 'object') return null;
      const row = raw as { appInstalled?: unknown; planWarning?: unknown };
      return {
        appInstalled: row.appInstalled === true,
        planWarning: typeof row.planWarning === 'string' && row.planWarning ? row.planWarning : undefined,
      };
    },
    async stageAuthPaste(body) {
      const path = await options.bridge.invoke('stage_auth_paste', { body });
      if (typeof path !== 'string' || path === '') {
        throw new Error('AnyPick could not store the pasted auth JSON.');
      }
      return path;
    },
    async forgetAuthPaste(path) {
      if (!path) return;
      await options.bridge.invoke('forget_auth_paste', { path });
    },
  };

  return {
    port,
    pending: () => new Set(inFlight),
    lookup: (requestId) => flights.get(requestId),
    settle,
  };
}
