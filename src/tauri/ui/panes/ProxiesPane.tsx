import { useEffect, useRef, useState } from 'react';

import type { ProxyRow, Snapshot } from '../lib/contract';
import {
  canRestartProxy,
  disambiguatedProxyNames,
  friendlyProxyStatusSubtitle,
  proxyStatus,
  proxyTokenSubject,
  servicePort,
} from '../lib/model';
import type { ServicePort } from '../lib/service';
import { ProviderIcon } from '../shell/ProviderIcon';
import { ProxyDetailSheet, ProxyGoneSheet } from './ProxyDetailSheet';
import { Symbol } from '../shell/Symbol';
import { Chevron } from '../ui/Chevron';
import { MenuItem, RowMenu } from '../ui/RowMenu';
import {
  Button,
  EmptyState,
  Group,
  Pane,
  PaneActions,
  Row,
  RowActions,
  RowText,
  StatusLabel,
} from '../ui/primitives';

/**
 * Proxies: the hub on top, then one relay per saved account.
 *
 * Lifecycle lives in the row that owns the service, not in a pane toolbar, so
 * a list with twenty services below it still shows what each one is doing. The
 * hub is reported separately because it is not one of the per-account relays,
 * and folding it in makes the running count mean two different things.
 */
export function ProxiesPane({
  service,
  snapshot,
  query = '',
  busy = new Set<string>(),
  onNavigate,
  onOpenModels,
  onClearQuery,
  proxyResults = {},
}: {
  service: ServicePort;
  snapshot: Snapshot;
  query?: string;
  /** Requests the engine has not answered; the engine is single-flight. */
  busy?: ReadonlySet<string>;
  /**
   * The engine's last answer per relay, when it belonged to that relay.
   *
   * A refused Stop or Rotate changes what the user thought they had set, so it
   * has to land on the destination the command came from — the same rule the hub
   * source rows follow. A pane-level banner would say something failed without
   * saying which relay is now in a state nobody chose.
   */
  proxyResults?: Record<string, { status: string; message: string } | undefined>;
  onNavigate?: (destination: 'accounts' | 'activity') => void;
  /** Reaches Published Models, which is not a sidebar destination on macOS. */
  onOpenModels?: () => void;
  onClearQuery?: () => void;
}) {
  /*
   * Stop and Rotate are confirmed per row; Start is not. macOS keeps the same
   * asymmetry (`ProxiesView.swift:73`, `:90`), so the pane owns both dialogs
   * rather than the row opening one — a confirmation belongs to the surface
   * that can lay it out, not to the control that asked for it.
   */
  const [pendingStop, setPendingStop] = useState<ProxyRow | null>(null);
  /*
   * The detail destination. `ProxyDetailView` on macOS; a dialog here, because a
   * webview has no navigation stack — the same trade `AccountDetailSheet` makes.
   *
   * `null` is the closed state and the id is not, so a relay deleted while its
   * sheet is open reads as "no longer available" rather than as a form of
   * undefined fields.
   */
  const [inspectingId, setInspectingId] = useState<string | null>(null);
  const [pendingRotate, setPendingRotate] = useState<ProxyRow | null>(null);
  const [pendingStopAll, setPendingStopAll] = useState(false);

  const hub = snapshot.proxies.find((proxy) => proxy.providerId === 'proxy-hub');
  const needle = query.trim().toLocaleLowerCase();
  const relays = snapshot.proxies.filter((proxy) => {
    if (proxy.providerId === 'proxy-hub') return false;
    const haystack = `${proxy.label} ${proxy.detail} ${proxy.address ?? ''}`.toLocaleLowerCase();
    return !needle || haystack.includes(needle);
  });
  const names = disambiguatedProxyNames(relays);

  const runningCount = relays.filter((proxy) => proxy.running).length;
  const stoppedCount = relays.filter((proxy) => !proxy.running).length;
  const hasRunning = runningCount > 0;

  return (
    <Pane title="Proxies" testId="proxies">
      {hub ? (
        <Group
          title="Proxy Hub"
          footer="A unified local endpoint combining published models across your accounts."
          testId="proxies-hub-group"
        >
          <Row testId="proxies-hub-summary">
            {/* macOS puts only a `DesignStatusLabel` in this row's `trailing`
                (`ProxiesView.swift:120`) — the hub's own lifecycle lives in the
                Proxy Hub pane it navigates to. Five controls here were the same
                clutter the account rows had. */}
            <span className="text-secondary" aria-hidden="true">
              <Symbol name="point.3.connected.trianglepath.dotted" size={22} />
            </span>
            <RowText
              title="Proxy Hub"
              detail={
                hub.address
                  ? `${hub.address} · ${snapshot.hubPublishedModels.length} model${
                      snapshot.hubPublishedModels.length === 1 ? '' : 's'
                    } published`
                  : 'One address · included accounts publish models'
              }
            />
            <RowActions>
              <StatusLabel status={proxyStatus(hub)} />
              {/*
                Published Models is a destination, not a command, so it wears a
                chevron the way a `NavigationLink` does — macOS reaches it from
                Proxy Hub, and this is the shortcut to that pane.
              */}
              <Button
                kind="quiet"
                testId="proxies-hub-models"
                disabled={snapshot.hubPublishedModels.length === 0}
                onClick={() => onOpenModels?.()}
              >
                Published Models
              </Button>
            </RowActions>
          </Row>
        </Group>
      ) : null}

      {relays.length === 0 ? (
        <EmptyState
          title={needle ? 'No Results' : 'No Account Proxies'}
          detail={
            needle
              ? `No proxies match “${query.trim()}”.`
              : 'Saved accounts that can proxy appear here even when stopped. Add one under Accounts & APIs.'
          }
          actionTitle={needle ? 'Clear Search' : 'Open Accounts & APIs'}
          onAction={needle ? onClearQuery : () => onNavigate?.('accounts')}
          testId="proxies-empty"
        />
      ) : (
        <Group
          title="Account Proxies"
          footer="All endpoints stay on this machine and require authentication. Select a proxy to view its address and API key."
          testId="proxies-list"
        >
          {relays.map((proxy) => (
            <ProxyRowView
              key={proxy.id}
              proxy={proxy}
              name={names.get(proxy.id) ?? proxy.label}
              revision={snapshot.revision}
              service={service}
              busy={busy}
              onStop={setPendingStop}
              onRotate={setPendingRotate}
              onInspect={setInspectingId}
            />
          ))}
        </Group>
      )}

      {relays.length > 0 ? (
        <PaneActions>
          <Button
            kind="secondary"
            testId="proxies-start-all"
            disabled={stoppedCount === 0 || busy.size > 0}
            onClick={() => service.mutate('proxy-start-all', { name: 'all' })}
          >
            {runningCount === 0 ? 'Start All Proxies' : 'Start Remaining Proxies'}
          </Button>
          <Button
            kind="secondary"
            testId="proxies-restart"
            disabled={!hasRunning || busy.size > 0}
            onClick={() => service.mutate('proxy-restart-all', { name: 'all' })}
          >
            Restart Running
          </Button>
          <Button
            kind="destructive"
            testId="proxies-stop-all"
            disabled={!hasRunning || busy.size > 0}
            onClick={() => setPendingStopAll(true)}
          >
            Stop All…
          </Button>
        </PaneActions>
      ) : null}

      {inspectingId ? (
        (() => {
          const inspected = snapshot.proxies.find((proxy) => proxy.id === inspectingId);
          return inspected ? (
            <ProxyDetailSheet
              proxy={inspected}
              snapshot={snapshot}
              service={service}
              busy={busy}
              revision={snapshot.revision}
              result={proxyResults[inspected.id] ?? null}
              onNavigate={onNavigate}
              onClose={() => setInspectingId(null)}
            />
          ) : (
            <ProxyGoneSheet onClose={() => setInspectingId(null)} />
          );
        })()
      ) : null}

      {pendingStop ? (
        <ConfirmDialog
          title={`Stop ${names.get(pendingStop.id) ?? pendingStop.label}?`}
          message="Apps using this proxy will not connect until it restarts."
          confirmTitle="Stop Proxy"
          testId={`proxies.stop-confirm-${pendingStop.id}`}
          onCancel={() => setPendingStop(null)}
          onConfirm={() => {
            service.invokeAction(pendingStop.toggleActionId ?? '', snapshot.revision);
            setPendingStop(null);
          }}
        />
      ) : null}

      {pendingRotate ? (
        <ConfirmDialog
          title={`Rotate API Key for ${names.get(pendingRotate.id) ?? pendingRotate.label}?`}
          message="Apps using the current API key will be disconnected until you update them with the new key."
          confirmTitle="Rotate Key"
          testId={`proxies.rotate-confirm-${pendingRotate.id}`}
          onCancel={() => setPendingRotate(null)}
          onConfirm={() => {
            service.invokeAction(pendingRotate.rotateActionId ?? '', snapshot.revision);
            setPendingRotate(null);
          }}
        />
      ) : null}

      {pendingStopAll ? (
        <ConfirmDialog
          title="Stop All Proxies?"
          message="Apps using these proxies will not connect until their services restart."
          confirmTitle="Stop All"
          destructive
          onCancel={() => setPendingStopAll(false)}
          onConfirm={() => {
            setPendingStopAll(false);
            service.mutate('proxy-stop-all', { name: 'all' });
          }}
        />
      ) : null}
    </Pane>
  );
}

function copyProxyKey(service: ServicePort, proxy: ProxyRow) {
  const subject = proxyTokenSubject(proxy.id, proxy.providerId);
  if (!subject) return;
  service.mutate('proxy-token', subject);
}

function ProxyRowView({
  proxy,
  name,
  revision,
  service,
  busy,
  onStop,
  onRotate,
  onInspect,
}: {
  proxy: ProxyRow;
  name: string;
  revision: number;
  service: ServicePort;
  busy: ReadonlySet<string>;
  /** Stop is confirmed; Start is not. The two are not symmetric. */
  onStop: (proxy: ProxyRow) => void;
  onRotate: (proxy: ProxyRow) => void;
  /** Opens the detail destination. */
  onInspect: (proxyId: string) => void;
}) {
  const status = proxyStatus(proxy);
  const port = servicePort(proxy.address ?? proxy.detail) ?? 0;
  const restorable = canRestartProxy(proxy.running, status);
  const running = proxy.running;
  const idle = busy.size === 0;
  const hasRestart = running && Boolean(proxy.restartActionId);

  /*
   * One trailing control, as on macOS: `ProxiesView.swift:143` puts
   * `lifecycleAction(proxy)` alone in `trailing:`, and `:147` puts everything
   * else in the `contextMenu`.
   *
   * This row used to carry six controls — a status dot, the address, two
   * *identical* copy glyphs, Stop, Restart and Rotate. Two adjacent glyphs that
   * look the same and copy different things is the kind of clutter that makes
   * a list unreadable, and macOS never had it.
   *
   * The address has moved to the detail sheet, which is where macOS keeps it
   * (`:86`) and where this pane's own footer always claimed it was. Keeping it
   * on the row was a workaround for a destination that did not exist.
   */
  const showMenu = running && (hasRestart || Boolean(proxy.address) || proxy.hasToken || Boolean(proxy.rotateActionId));

  return (
    <Row
      testId={`proxy-${proxy.id}`}
      warning={proxy.enabled && !proxy.running}
      onClick={() => onInspect(proxy.id)}
    >
      {/* `DesignProviderIcon` (`ProxiesView.swift:141`): the relay wears the
          mark of the provider it fronts, so a stopped row still says whose
          account it belongs to. */}
      <ProviderIcon provider={proxy.providerId} fallbackSymbol="server.rack" size={24} className="mr-1" />
      <RowText
        title={name}
        detail={friendlyProxyStatusSubtitle(status, port, proxy.detail)}
        issue={proxy.enabled && !proxy.running ? 'This proxy isn’t running.' : undefined}
      />
      <RowActions>
        {showMenu ? (
          <RowMenu label={`Actions for ${name}`} testId={`proxies.more-${proxy.id}`}>
            {(close) => (
              <>
                {hasRestart ? (
                  <MenuItem
                    testId={`proxies.inspector.restart-${proxy.id}`}
                    disabled={!restorable || !idle}
                    onSelect={() => {
                      close();
                      service.invokeAction(proxy.restartActionId ?? '', revision);
                    }}
                  >
                    Restart
                  </MenuItem>
                ) : null}
                {proxy.address ? (
                  <MenuItem
                    testId={`proxies.inspector.copy-${proxy.id}`}
                    onSelect={() => {
                      close();
                      void service.copy(proxy.address ?? '');
                    }}
                  >
                    Copy Address
                  </MenuItem>
                ) : null}
                {/* ADR 0006: the snapshot says a copy is possible, never the
                    value. The key arrives in a result payload, on request. */}
                {proxy.hasToken ? (
                  <MenuItem
                    testId={`proxies.inspector.copy-key-${proxy.id}`}
                    onSelect={() => {
                      close();
                      copyProxyKey(service, proxy);
                    }}
                  >
                    Copy API Key
                  </MenuItem>
                ) : null}
                {proxy.rotateActionId ? (
                  <MenuItem
                    testId={`proxies.inspector.rotate-key-${proxy.id}`}
                    disabled={!idle}
                    onSelect={() => {
                      close();
                      onRotate(proxy);
                    }}
                  >
                    Rotate API Key…
                  </MenuItem>
                ) : null}
              </>
            )}
          </RowMenu>
        ) : null}
        {/*
          macOS names this command twice — `proxies.row-action.<id>` on the
          context menu and `proxies.inspector.lifecycle` on the detail — for
          one button. There is one button here, so it carries both ids’
          meaning; the map records that rather than a second control that would
          send the same command twice.
        */}
        <Button
          kind={running ? 'destructive' : 'secondary'}
          testId={`proxies.row-action.${proxy.id}`}
          disabled={!idle || !proxy.toggleActionId}
          ariaLabel={`${running ? 'Stop' : 'Start'} ${name}`}
          title={`${running ? 'Stop' : 'Start'} ${name}`}
          onClick={() =>
            running ? onStop(proxy) : service.invokeAction(proxy.toggleActionId ?? '', revision)
          }
        >
          {running ? 'Stop' : 'Start'}
        </Button>
        {/* The row is a `NavigationLink` on macOS (`ProxiesView.swift:135`), so
            the disclosure chevron sits last, at the trailing edge. */}
        <Chevron />
      </RowActions>
    </Row>
  );
}

/**
 * `confirmationDialog`, not a custom modal card.
 *
 * Destructive work is confirmed before it happens. The confirm button is the
 * one that acts; cancel is inert and closes.
 */
export function ConfirmDialog({
  title,
  message,
  confirmTitle,
  destructive = false,
  onConfirm,
  onCancel,
  testId = 'confirm-dialog',
}: {
  title: string;
  message: string;
  confirmTitle: string;
  destructive?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
  testId?: string;
}) {
  // Focus starts on the confirmation, not Cancel. This is the one dialog
  // where the safe default is the wrong one: the user asked to destroy
  // something and is confirming a specific action, and macOS's
  // `confirmationDialog` gives the default key equivalent to the button they
  // chose. A stray Return must not land on Cancel.
  const ref = useConfirmFocus();
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-6 backdrop-blur-[1px]">
      <div
        ref={ref}
        role="alertdialog"
        aria-modal="true"
        aria-label={title}
        data-testid={testId}
        className="flex w-full max-w-[360px] flex-col gap-2.5 rounded-xl border border-separator-soft bg-canvas px-5 py-4 shadow-[0_16px_48px_rgba(0,0,0,0.36)]"
      >
        <h2 className="text-[13px] font-semibold text-text">{title}</h2>
        <p className="m-0 text-[12px] leading-[1.4] text-secondary">{message}</p>
        <div className="flex justify-end gap-2 pt-2">
          <Button kind="secondary" onClick={onCancel}>
            Cancel
          </Button>
          <Button kind={destructive ? 'destructive' : 'primary'} onClick={onConfirm} data-confirm>
            {confirmTitle}
          </Button>
        </div>
      </div>
    </div>
  );
}

/** Focuses the confirming button on mount and restores focus on unmount. */
function useConfirmFocus() {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const previous = document.activeElement as HTMLElement | null;
    ref.current?.querySelector<HTMLElement>('[data-confirm]')?.focus();
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        const cancel = ref.current?.querySelectorAll<HTMLElement>('button')[0];
        cancel?.click();
      }
    };
    ref.current?.addEventListener('keydown', onKeyDown);
    return () => {
      ref.current?.removeEventListener('keydown', onKeyDown);
      previous?.focus?.();
    };
  }, []);
  return ref;
}
