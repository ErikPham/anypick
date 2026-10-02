import { useCallback, useEffect, useMemo, useRef, useState, type MutableRefObject } from 'react';

import { resolveBridge, type BridgeEvent, type TrayBridge } from './lib/bridge';
import type { Destination, Snapshot } from './lib/contract';
import { createService, type ServiceHandle, type ServicePort } from './lib/service';
import {
  decodeFrame,
  errorText,
  type BackupPreviewFrame,
  type DoctorFrame,
  type LogsFrame,
  type ServiceFrame,
  type ServiceResult,
} from './lib/wire';
import { ui } from './i18n';
import {
  AccountsPane,
  ActivityPane,
  AppsPane,
  BackupsPane,
  ConfirmDialog,
  OverviewPane,
  ProxiesPane,
  ProxyHubPane,
  PublishedModelsPane,
} from './panes';

import { SettingsSheet } from './panes/SettingsSheet';
import type { AccountFlowResult } from './panes/AddAccountSheet';
import type { GatewayFlowResult } from './panes/AddGatewaySheet';
import type { ModelRolesResult } from './panes/AppRouteSheet';
import type { HubRowResult } from './panes/ProxyHubPane';

import { EmptyState, LoadingState } from './ui/primitives';
import { NoticeBanner, type Notice } from './shell/NoticeBanner';
import { Sidebar } from './shell/Sidebar';
import { Toolbar } from './shell/Toolbar';

type ServiceState = 'starting' | 'connected' | 'stopped' | 'error';

/** Destinations whose list is long enough to earn a search field. */
const SEARCHABLE: ReadonlySet<Destination> = new Set<Destination>([
  'apps',
  'accounts',
  'proxyHub',
  'proxies',
  'activity',
]);

export function App({
  bridge: injected,
  serviceRef,
}: {
  bridge?: TrayBridge;
  /**
   * Receives the live service handle. A test uses it to assert which requests
   * are still outstanding; production passes nothing and never reads it.
   */
  serviceRef?: MutableRefObject<ServiceHandle | null>;
} = {}) {
  // Resolved once, lazily, so a test can inject a bridge while production stays
  // strict about running inside the Tauri shell.
  const bridgeRef = useRef<TrayBridge | null>(injected ?? null);
  if (!bridgeRef.current) bridgeRef.current = resolveBridge();
  const bridge = bridgeRef.current;

  const [destination, setDestination] = useState<Destination>('overview');
  const [snapshot, setSnapshot] = useState<Snapshot | null>(null);
  const [state, setState] = useState<ServiceState>('starting');
  const [notice, setNotice] = useState<Notice | null>(null);
  const [query, setQuery] = useState('');
  const [takeover, setTakeover] = useState<{ actionId: string; message: string } | null>(null);
  const [settingsOpen, setSettingsOpen] = useState(false);
  // Settings → Show Getting Started has to un-hide the Overview checklist, and
  // the checklist reads localStorage once on mount. Bumping a key re-reads it
  // rather than making the two panes share a preference object.
  const [gettingStartedKey, setGettingStartedKey] = useState(0);
  const [logs, setLogs] = useState<Readonly<Record<string, LogsFrame>>>({});
  const [backupPreview, setBackupPreview] = useState<BackupPreviewFrame | null>(null);
  const [doctor, setDoctor] = useState<DoctorFrame | null>(null);
  // A replayed last_service_line can be older than a frame that already
  // arrived. Action ids are scoped to a revision, so an older snapshot must
  // not replace the one the window is acting on.
  const seenRevision = useRef(-1);

  const service = useMemo(
    () =>
      createService({
        bridge,
        // macOS is single-flight: `model.isBusy` disables the controls while a
        // mutation runs, because the engine accepts one at a time. The busy set
        // is what a pane reads to hold its own buttons.
        onBusyChange: (pending) => setBusy(pending),
        onError: (message) => setNotice({ kind: 'error', text: message }),
      }),
    [bridge],
  );
  const [busy, setBusy] = useState<ReadonlySet<string>>(() => new Set());
  // Every Add Account command's answer, routed to the sheet rather than to a
  // banner. macOS keeps the detected identity in the sheet's identity card and
  // a failed sign-in in an inline row with the ways back out; a banner over the
  // dialog says neither and covers the fields the user still has to fix.
  const [accountFlowResult, setAccountFlowResult] = useState<AccountFlowResult | null>(null);
  // The gateway sheet's Test Connection is a row of its own, not a banner: the
  // answer decides whether the endpoint on screen works, and a toast over the
  // dialog would hide the field the user has to fix.
  const [gatewayFlowResult, setGatewayFlowResult] = useState<GatewayFlowResult | null>(null);
  const [modelRolesResult, setModelRolesResult] = useState<ModelRolesResult | null>(null);
  const [hubRowResult, setHubRowResult] = useState<HubRowResult | null>(null);

  // The handle is a `useMemo` value, so writing a ref during render is the
  // documented way to publish it (React docs, "UseRef" → "Avoiding
  // recreating the ref contents"). The alternative, a `useEffect`, leaves a
  // window where a caller reads null after the first paint.
  useEffect(() => {
    if (serviceRef) serviceRef.current = service;
  }, [service, serviceRef]);

  const applyFrame = useCallback(
    (frame: ServiceFrame | null) => {
      if (!frame) return;
      if (frame.kind === 'snapshot' || frame.kind === 'snapshot-event') {
        if (frame.snapshot.revision <= seenRevision.current) return;
        seenRevision.current = frame.snapshot.revision;
        setSnapshot(frame.snapshot);
        setState('connected');
        return;
      }
      if (frame.kind === 'usage-event') {
        // Quota rides its own frame between snapshots. An older reading must
        // not replace the one that belongs to the snapshot on screen.
        setSnapshot((current) => {
          if (!current || frame.revision < current.revision) return current;
          return { ...current, usage: frame.usage };
        });
        return;
      }
      if (frame.kind === 'logs') {
        const flight = service.lookup(frame.logs.requestId);
        if (flight?.kind === 'logs') {
          service.settle(frame.logs.requestId);
          setLogs((current) => ({ ...current, [frame.logs.proxyId]: frame.logs }));
        }
        return;
      }
      if (frame.kind === 'backup-preview') {
        const flight = service.lookup(frame.preview.requestId);
        if (flight?.kind === 'backup-inspect') {
          service.settle(frame.preview.requestId);
          setBackupPreview(frame.preview);
        }
        return;
      }
      if (frame.kind === 'doctor') {
        // The report carries no request id, so there is nothing to settle.
        // A healthy run is silent; findings are what the user asked for.
        setDoctor(frame.report);
        if (frame.report.ok) {
          setNotice({ kind: 'info', text: 'Doctor finished. Nothing needs attention.', suggestions: [] });
        }
        return;
      }
      if (frame.kind === 'result') {
        // Read the command before settle drops it. A result with no command
        // cannot be told apart from a snapshot refresh.
        const flight = service.lookup(frame.result.requestId);
        // A control is released by the engine's own answer, never by a timer:
        // exactly one terminal frame arrives per request, and a timeout would
        // re-enable a button whose mutation had not landed.
        service.settle(frame.result.requestId);
        const result = frame.result;
        if (result.code === 'CLIENT_CONFIG_FOREIGN' && flight?.kind === 'invoke') {
          // Only the invoke that was refused may be retried. Confirming a
          // different action would be the takeover the refusal exists to stop.
          setTakeover({
            actionId: flight.actionId,
            message: result.message || 'AnyPick needs confirmation before it replaces settings it does not own.',
          });
          return;
        }
        if (result.status === 'success' && flight?.kind === 'mutate' && flight.operation === 'proxy-token') {
          const key = typeof result.payload?.apiKey === 'string' ? result.payload.apiKey : '';
          // The secret goes to the clipboard only. The notice names the
          // action, never the key (ADR 0006).
          if (!key) {
            setNotice({ kind: 'error', text: result.message || 'Couldn’t copy API key.', suggestions: [] });
            return;
          }
          // `navigator.clipboard` is undefined when the context is not secure,
          // and reading the property would throw inside this event callback
          // with no error boundary above it. Test setup always stubs a
          // clipboard, so no test can catch the unguarded form.
          const clipboard = navigator.clipboard;
          if (!clipboard) {
            setNotice({ kind: 'error', text: 'Couldn’t copy API key.', suggestions: [] });
            return;
          }
          void clipboard.writeText(key).then(
            () => setNotice({ kind: 'info', text: 'Copied API key.', suggestions: [] }),
            () => setNotice({ kind: 'error', text: 'Couldn’t copy API key.', suggestions: [] }),
          );
          return;
        }
        if (result.status === 'success' && flight?.kind === 'doctor') {
          setNotice({ kind: 'info', text: result.message || 'Doctor finished.', suggestions: [] });
          return;
        }
        if (flight?.kind === 'mutate' && HUB_ROW_OPERATIONS.has(flight.operation)) {
          // A refused hub source toggle belongs to the row that sent it. A
          // banner over the pane would say something went wrong without saying
          // which switch is now in a state the user did not choose.
          setHubRowResult({ operation: flight.operation, result });
          return;
        }
        if (flight?.kind === 'model-roles') {
          // A model pin that the engine refused must not close the sheet: the
          // user is mid-flow with a choice on screen, and a silent close reads
          // as the choice having been applied.
          setModelRolesResult({ clientId: flight.clientId, result });
          if (result.status === 'success') setModelRolesResult(null);
          return;
        }
        if (flight?.kind === 'mutate' && flight.operation === 'gateway-test') {
          setGatewayFlowResult({ operation: flight.operation, result });
          return;
        }
        if (result.status === 'success' && flight?.kind === 'mutate' && SPOKEN_RESULT.has(flight.operation)) {
          if (flight.operation === 'account-backup-forget' || flight.operation === 'account-backup-import') {
            setBackupPreview(null);
          }
          // A backup has no row of its own. The engine's sentence is the only
          // proof the write happened.
          setNotice({ kind: 'info', text: result.message || 'Action completed.', suggestions: [] });
          return;
        }
        if (flight?.kind === 'mutate' && ACCOUNT_FLOW.has(flight.operation)) {
          setAccountFlowResult({ operation: flight.operation, result });
          return;
        }
        // `cancelled` is the user's decision, not a failure, and other
        // successes stay silent because the row's own state already speaks.
        if (result.status === 'error') {
          setNotice({
            kind: 'error',
            text: result.message || 'Action failed.',
            suggestions: result.suggestions ?? [],
            code: result.code,
          });
        }
        return;
      }
      if (frame.kind === 'protocol-error') {
        if (frame.requestId) service.settle(frame.requestId);
        setNotice({ kind: 'error', text: frame.message, suggestions: [], code: frame.code });
      }
    },
    [service],
  );

  useEffect(() => {
    const unlisten: Array<() => void> = [];
    let cancelled = false;

    void bridge
      .listen('service-line', (event: BridgeEvent) => {
        applyFrame(decodeFrame(String(event.payload ?? '')));
      })
      .then(async (off) => {
        if (cancelled) {
          off();
          return;
        }
        unlisten.push(off);
        // The service can emit the only full snapshot before this listener
        // exists, so the last line is replayed once one is attached. Without
        // this the window shows "connecting" until the next mutation.
        // Mount is not a settled state: this resolves after the first paint,
        // so the state it sets is flushed inside `act` by the caller that
        // rendered us. Anything else warns about an update nobody wrapped.
        const line = await bridge.invoke('last_service_line');
        if (cancelled) return;
        if (typeof line === 'string' && line) applyFrame(decodeFrame(line));
      })
      .catch((error: unknown) => {
        setState('error');
        setNotice({ kind: 'error', text: errorText(error) });
      });

    void bridge
      .listen('service-state', (event: BridgeEvent) => {
        if (cancelled) return;
        const payload = event.payload as { phase?: string; message?: string } | string;
        const phase = String(typeof payload === 'string' ? payload : (payload?.phase ?? '')).toLowerCase();
        const message = typeof payload === 'string' ? '' : (payload?.message ?? '');
        if (phase.includes('error') || phase === 'degraded') {
          setState('error');
          if (message) setNotice({ kind: 'error', text: message });
        } else if (phase.includes('stopped') || phase.includes('exit')) {
          setState('stopped');
        } else if (phase.includes('ready') || phase.includes('running')) {
          setState('connected');
        } else {
          setState('starting');
        }
      })
      .then((off) => {
        if (cancelled) off();
        else unlisten.push(off);
      })
      // Without this the rejection is unhandled and the window stays in
      // "connecting" forever, because no other frame updates this state.
      .catch((error: unknown) => {
        setState('error');
        setNotice({ kind: 'error', text: errorText(error) });
      });

    return () => {
      cancelled = true;
      for (const off of unlisten) off();
    };
  }, [applyFrame, bridge]);

  // The search box belongs to the pane that owns it, so it clears on navigation
  // rather than carrying "gemini" into a list of proxies.
  useEffect(() => setQuery(''), [destination]);

  // Published Models is a detail of Proxy Hub, not a sidebar destination.
  // macOS pushes it onto the detail stack; a webview has no NavigationStack, so
  // it is a route the shell owns and the sidebar cannot select.
  const [publishedModelsOpen, setPublishedModelsOpen] = useState(false);
  const [selectedAppId, setSelectedAppId] = useState<string | null>(null);
  const openPublishedModels = useCallback(() => {
    setPublishedModelsOpen(true);
    setQuery('');
  }, []);
  const closePublishedModels = useCallback(() => setPublishedModelsOpen(false), []);

  // Leaving the hub or the proxies pane closes it, so Back never lands on a
  // detail whose owner is not on screen.
  const contentRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    setPublishedModelsOpen(false);
    setSelectedAppId(null);
  }, [destination]);

  const selectedApp = useMemo(() => {
    if (destination !== 'apps' || !selectedAppId || !snapshot) return null;
    return snapshot.actions.find((action) => action.clientId === selectedAppId);
  }, [destination, selectedAppId, snapshot]);

  const activeTitle = publishedModelsOpen
    ? 'Published Models'
    : selectedApp
    ? selectedApp.client
    : DestinationLabel[destination];

  const activeBack = publishedModelsOpen
    ? () => setPublishedModelsOpen(false)
    : selectedApp
    ? () => setSelectedAppId(null)
    : undefined;

  useEffect(() => {
    const paneTitle = activeTitle;
    if (typeof document !== 'undefined') {
      document.title = paneTitle ? `${paneTitle} — AnyPick` : 'AnyPick';
    }
    if (contentRef.current) {
      if (typeof contentRef.current.scrollTo === 'function') {
        contentRef.current.scrollTo({ top: 0 });
      } else {
        contentRef.current.scrollTop = 0;
      }
    }
  }, [destination, publishedModelsOpen, selectedAppId, activeTitle]);

  const runningCount = useMemo(
    () => (snapshot?.proxies ?? []).filter((proxy) => proxy.running).length,
    [snapshot],
  );

  return (
    // The shell is a grid, not a flex row with a fixed sidebar. `100dvh`
    // rather than `100%`: a percentage height becomes `auto` when the
    // webview's parent height is indefinite, which leaves both columns only as
    // tall as their text and the scroll panes collapse.
    <div className="grid h-[100dvh] min-h-0 w-full grid-cols-[216px_minmax(0,1fr)] overflow-hidden bg-canvas max-[780px]:grid-cols-[188px_minmax(0,1fr)]">
      <Sidebar
        destination={destination}
        onSelect={setDestination}
        state={state}
        runningCount={runningCount}
        onOpenSettings={() => setSettingsOpen(true)}
      />
      <main className="flex min-h-0 min-w-0 flex-col bg-canvas">
        <Toolbar
          title={activeTitle}
          onBack={activeBack}
          searchable={(SEARCHABLE.has(destination) && !selectedApp) || publishedModelsOpen}
          query={query}
          onQuery={setQuery}
          onRefresh={() => service.port.refresh()}
          snapshot={snapshot}
        />
        <div
          ref={contentRef}
          className="min-h-0 flex-1 overflow-auto [scrollbar-color:var(--color-tertiary)_transparent] [scrollbar-width:thin]"
        >
          {notice ? <NoticeBanner notice={notice} onDismiss={() => setNotice(null)} /> : null}
          {takeover ? (
            <ConfirmDialog
              title="Manage with AnyPick?"
              message={takeover.message}
              confirmTitle="Manage with AnyPick"
              onCancel={() => setTakeover(null)}
              onConfirm={() => {
                const actionId = takeover.actionId;
                setTakeover(null);
                if (snapshot) service.port.invokeAction(actionId, snapshot.revision, true);
              }}
            />
          ) : null}
          {snapshot ? (
            <>
              <DestinationPane
                destination={destination}
                snapshot={snapshot}
                query={query}
                service={service.port}
                busy={busy}
                logs={logs}
                backupPreview={backupPreview}
                onNavigate={setDestination}
                onClearQuery={() => setQuery('')}
                publishedModelsOpen={publishedModelsOpen}
                onOpenPublishedModels={openPublishedModels}
                selectedAppId={selectedAppId}
                onSelectApp={setSelectedAppId}
                accountFlowResult={accountFlowResult}
                onClearFlowResult={() => setAccountFlowResult(null)}
                gatewayFlowResult={gatewayFlowResult}
                modelRolesResult={modelRolesResult}
                hubRowResult={hubRowResult}
                gettingStartedKey={gettingStartedKey}
              />
              {settingsOpen ? (
                <SettingsSheet
                  settings={snapshot.settings}
                  snapshot={snapshot}
                  service={service.port}
                  busy={busy.size > 0}
                  report={doctor}
                  onClose={() => setSettingsOpen(false)}
                  onNavigate={setDestination}
                  onShowGettingStarted={() => setGettingStartedKey((n) => n + 1)}
                />
              ) : null}
            </>
          ) : state === 'error' || state === 'stopped' ? (
            // macOS gives a service that will not load its own state with a
            // retry. Rendering the loading spinner here instead left the window
            // saying "Connecting to the service…" forever, with no way out and
            // nothing to tell the user what happened.
            <EmptyState
              title={state === 'stopped' ? 'AnyPick service stopped' : 'AnyPick could not load'}
              detail={
                state === 'stopped'
                  ? 'The bundled AnyPick service is not running. Restart AnyPick to try again.'
                  : 'The bundled AnyPick service did not provide a valid snapshot. Try again or reinstall AnyPick.'
              }
              actionTitle="Try Again"
              onAction={() => {
                setState('starting');
                void service.port.refresh();
              }}
              testId="state.error"
            />
          ) : (
            <LoadingState label={ui.connecting} />
          )}
        </div>
      </main>
    </div>
  );
}

const DestinationLabel: Record<Destination, string> = {
  overview: 'Overview',
  apps: 'Apps',
  accounts: 'Accounts & APIs',
  proxyHub: 'Proxy Hub',
  proxies: 'Proxies',
  activity: 'Activity',
  backups: 'Backups',
};

/**
 * Operations whose failure is the row's own.
 *
 * A refused toggle changes what the user thought they had set, so the row that
 * sent it has to say so where they are looking.
 */
const HUB_ROW_OPERATIONS = new Set(['hub-source-toggle', 'hub-rotation']);

const SPOKEN_RESULT = new Set([
  'account-backup-all',
  'account-backup-export',
  'account-backup-import',
  'account-backup-forget',
]);

/**
 * The operations the Add Account sheet owns.
 *
 * Each one has an answer the sheet must show in place: a detected identity, a
 * failed sign-in with the ways back out, a pasted document that already
 * exists. A banner would cover the dialog holding those fields.
 */
const ACCOUNT_FLOW = new Set([
  'account-detect',
  'account-login',
  'account-save',
  'account-save-input',
  'account-save-auth',
  'account-clear',
]);


function DestinationPane({
  destination,
  snapshot,
  query,
  service,
  busy,
  logs,
  backupPreview,
  onNavigate,
  onClearQuery,
  publishedModelsOpen,
  onOpenPublishedModels,
  selectedAppId,
  onSelectApp,
  accountFlowResult,
  onClearFlowResult,
  gatewayFlowResult,
  modelRolesResult,
  hubRowResult,
  gettingStartedKey,
}: {
  destination: Destination;
  snapshot: Snapshot;
  query: string;
  service: ServicePort;
  busy: ReadonlySet<string>;
  logs: Readonly<Record<string, LogsFrame>>;
  backupPreview: BackupPreviewFrame | null;
  onNavigate: (destination: Destination) => void;
  onClearQuery: () => void;
  publishedModelsOpen: boolean;
  onOpenPublishedModels: () => void;
  selectedAppId?: string | null;
  onSelectApp?: (appId: string | null) => void;
  accountFlowResult: AccountFlowResult | null;
  onClearFlowResult: () => void;
  gatewayFlowResult: GatewayFlowResult | null;
  modelRolesResult: ModelRolesResult | null;
  hubRowResult: HubRowResult | null;
  gettingStartedKey: number;
}) {
  switch (destination) {
    case 'overview':
      return <OverviewPane key={`overview-${gettingStartedKey}`} snapshot={snapshot} onNavigate={onNavigate} />;
    case 'apps':
      return (
        <AppsPane
          service={service}
          snapshot={snapshot}
          query={query}
          busy={busy}
          modelRolesResult={modelRolesResult}
          onClearQuery={onClearQuery}
          selectedClientId={selectedAppId}
          onSelectClient={onSelectApp}
        />
      );
    case 'accounts':
      return (
        <AccountsPane
          service={service}
          snapshot={snapshot}
          query={query}
          busy={busy}
          accountFlowResult={accountFlowResult}
          onClearFlowResult={onClearFlowResult}
          gatewayFlowResult={gatewayFlowResult}
        />
      );
    case 'proxyHub':
      return publishedModelsOpen ? (
        <PublishedModelsPane
          service={service}
          snapshot={snapshot}
          query={query}
          busy={busy}
          onNavigate={onNavigate}
          onClearQuery={onClearQuery}
        />
      ) : (
        <ProxyHubPane
          service={service}
          snapshot={snapshot}
          query={query}
          busy={busy}
          hubRowResult={hubRowResult}
          onNavigate={onNavigate}
          onOpenModels={onOpenPublishedModels}
        />
      );
    case 'proxies':
      return publishedModelsOpen ? (
        <PublishedModelsPane
          service={service}
          snapshot={snapshot}
          query={query}
          busy={busy}
          onNavigate={onNavigate}
          onClearQuery={onClearQuery}
        />
      ) : (
        <ProxiesPane
          service={service}
          snapshot={snapshot}
          query={query}
          busy={busy}
          onNavigate={onNavigate}
          onOpenModels={onOpenPublishedModels}
          onClearQuery={onClearQuery}
        />
      );
    case 'activity':
      return <ActivityPane snapshot={snapshot} service={service} logs={logs} query={query} busy={busy} />;
    case 'backups':
      return <BackupsPane service={service} snapshot={snapshot} preview={backupPreview} busy={busy} />;
  }
}
