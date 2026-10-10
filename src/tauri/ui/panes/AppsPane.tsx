import { useMemo, useState } from 'react';

import type { Snapshot } from '../lib/contract';
import {
  refreshModels,
  resetClient,
} from '../lib/desktop-actions';
import {
  addApp,
  availableAppsToAdd,
  effectiveConfiguredAppIDs,
  removeApp,
  storeConfiguredAppIDs,
  storeRemovedAppIDs,
  storedConfiguredAppIDs,
  storedRemovedAppIDs,
} from '../lib/app-preferences';
import { isAppRouteConfigured } from '../lib/model';
import type { ServicePort } from '../lib/service';
import {
  Button,
  EmptyState,
  Group,
  Pane,
  PaneActions,
  Row,
  RowActions,
  RowText,
} from '../ui/primitives';
import { MenuDivider, MenuItem, RowMenu } from '../ui/RowMenu';
import { ProviderIcon } from '../shell/ProviderIcon';
import {
  appEntries,
  appIssue,
  appSubtitle,
  appTrailing,
  appsSectionTitle,
  type AppEntry,
} from '../lib/app-entry';
import { ConfirmDialog } from './ProxiesPane';
import { AddAppSheet } from './AddAppSheet';
import type { ModelRolesResult } from './AppRouteSheet';
import { AppRouteSheet } from './AppRouteSheet';
import { AppDetailPane } from './AppDetailPane';

export function AppsPane({
  service,
  snapshot,
  query = '',
  busy = new Set<string>(),
  modelRolesResult = null,
  onClearQuery,
  selectedClientId: propSelectedClientId,
  onSelectClient,
}: {
  service: ServicePort;
  snapshot: Snapshot;
  query?: string;
  busy?: ReadonlySet<string>;
  modelRolesResult?: ModelRolesResult | null;
  onClearQuery?: () => void;
  selectedClientId?: string | null;
  onSelectClient?: (clientId: string | null) => void;
}) {
  const allAppIds = useMemo(() => uniqueClientIds(snapshot.actions), [snapshot.actions]);
  const [preferences, setPreferences] = useState(() => ({
    configured: storedConfiguredAppIDs(),
    removed: storedRemovedAppIDs(),
  }));
  const configured = useMemo(
    () =>
      effectiveConfiguredAppIDs({
        allAppIds,
        actions: snapshot.actions,
        stored: preferences.configured,
        removed: preferences.removed,
      }),
    [allAppIds, preferences, snapshot.actions],
  );
  const addable = useMemo(() => availableAppsToAdd(allAppIds, configured), [allAppIds, configured]);
  const visible = useMemo(
    () => snapshot.actions.filter((action) => configured.includes(action.clientId)),
    [configured, snapshot.actions],
  );
  const needle = query.trim().toLocaleLowerCase();
  const entries = useMemo(
    () =>
      appEntries(visible).filter((app) => {
        if (!needle) return true;
        const haystack = `${app.client} ${appSubtitle(app)}`.toLocaleLowerCase();
        return haystack.includes(needle);
      }),
    [needle, visible],
  );

  const [internalSelectedClientId, setInternalSelectedClientId] = useState<string | null>(null);
  const selectedClientId =
    propSelectedClientId !== undefined ? propSelectedClientId : internalSelectedClientId;

  const selectClient = (id: string | null) => {
    setInternalSelectedClientId(id);
    onSelectClient?.(id);
  };

  const [resetClientId, setResetClientId] = useState<string | null>(null);
  const [changingClientId, setChangingClientId] = useState<string | null>(null);
  const [adding, setAdding] = useState(false);
  const [removingClientId, setRemovingClientId] = useState<string | null>(null);

  const applyPreferences = (next: { configured: string[]; removed: string[] }) => {
    storeConfiguredAppIDs(next.configured);
    storeRemovedAppIDs(next.removed);
    setPreferences(next);
  };

  const changingRows = useMemo(
    () => (changingClientId ? snapshot.actions.filter((row) => row.clientId === changingClientId) : []),
    [changingClientId, snapshot.actions],
  );

  if (selectedClientId) {
    const selectedApp =
      entries.find((app) => app.clientId === selectedClientId) ??
      appEntries(snapshot.actions).find((app) => app.clientId === selectedClientId);

    if (selectedApp) {
      return (
        <AppDetailPane
          app={selectedApp}
          service={service}
          snapshot={snapshot}
          busy={busy}
          modelRolesResult={modelRolesResult}
          onBack={() => selectClient(null)}
          onReset={() => setResetClientId(selectedApp.clientId)}
          onRemove={() => setRemovingClientId(selectedApp.clientId)}
        />
      );
    }
  }

  return (
    <Pane title="Apps" testId="apps">
      {entries.length === 0 ? (
        <EmptyState
          title={needle ? 'No Results' : 'No Apps Configured'}
          detail={
            needle
              ? 'No apps match your search.'
              : 'Add the coding apps you want to configure with AnyPick accounts and proxies.'
          }
          actionTitle={needle ? 'Clear Search' : undefined}
          onAction={needle ? onClearQuery : undefined}
          testId="apps-empty"
          actions={
            needle || addable.length === 0 ? null : (
              <Button kind="primary" testId="apps.empty.add" onClick={() => setAdding(true)}>
                Add App…
              </Button>
            )
          }
        />
      ) : null}

      {entries.length > 0 ? (
        <Group title={appsSectionTitle(entries.length, Boolean(needle))} testId="apps.list">
          {entries.map((app) => (
            <AppListRow
              key={app.clientId}
              app={app}
              busy={busy}
              service={service}
              onClick={() => selectClient(app.clientId)}
              onChangeConnection={() => setChangingClientId(app.clientId)}
              onReset={() => setResetClientId(app.clientId)}
              onRemove={() => setRemovingClientId(app.clientId)}
            />
          ))}
        </Group>
      ) : null}

      <PaneActions>
        <Button
          kind="secondary"
          disabled={busy.size > 0}
          testId="apps.add"
          onClick={() => setAdding(true)}
        >
          Add App…
        </Button>
      </PaneActions>

      {changingClientId && changingRows.length > 0 ? (
        <AppRouteSheet
          clientId={changingClientId}
          rows={changingRows}
          service={service}
          snapshot={snapshot}
          busy={busy}
          modelRolesResult={modelRolesResult}
          onClose={() => setChangingClientId(null)}
        />
      ) : null}

      {adding ? (
        <AddAppSheet
          available={addable}
          onClose={() => setAdding(false)}
          onAdd={(clientId) => {
            applyPreferences(addApp(preferences.configured ?? configured, preferences.removed, clientId));
            setAdding(false);
          }}
        />
      ) : null}

      {removingClientId ? (
        <ConfirmDialog
          title={`Remove ${clientName(snapshot.actions, removingClientId)}?`}
          message="AnyPick stops listing this app. Its own settings are untouched, and you can add it back at any time."
          confirmTitle="Remove App"
          destructive
          testId="app.remove.dialog"
          onCancel={() => setRemovingClientId(null)}
          onConfirm={() => {
            const next = removeApp(preferences.configured ?? configured, preferences.removed, removingClientId);
            applyPreferences(next);
            setRemovingClientId(null);
          }}
        />
      ) : null}

      {resetClientId ? (
        <ConfirmDialog
          title={`Disconnect ${resetClientId}?`}
          message="AnyPick removes the route it wrote for this app."
          confirmTitle="Disconnect"
          destructive
          testId="apps-disconnect-dialog"
          onCancel={() => setResetClientId(null)}
          onConfirm={() => {
            const action = resetClient(resetClientId);
            setResetClientId(null);
            service.mutate(action.operation, action.extra);
          }}
        />
      ) : null}
    </Pane>
  );
}

function AppListRow({
  app,
  busy,
  service,
  onClick,
  onChangeConnection,
  onReset,
  onRemove,
}: {
  app: AppEntry;
  busy: ReadonlySet<string>;
  service: ServicePort;
  onClick: () => void;
  onChangeConnection: () => void;
  onReset: () => void;
  onRemove: () => void;
}) {
  const [refreshOpen, setRefreshOpen] = useState(false);
  const disabled = Boolean(app.issue) && app.issue !== 'No login chosen for this app.';
  const refresh = refreshModels(app.selected?.sourceId ?? '');
  const trailing = appTrailing(app);
  const isManaged = app.managed;
  const isConfigured = Boolean(app.selected && app.selected.selected && app.selected.routeKind);

  return (
    <>
      <Row
        testId={`apps-row-${app.clientId}`}
        warning={disabled}
        onClick={onClick}
      >
        <ProviderIcon provider={app.clientId} size={26} className="mr-1.5" />
        <RowText title={app.client} detail={appSubtitle(app)} issue={appIssue(app)} />
        <RowActions>
          {isManaged ? (
            <span className="rounded-control bg-badge px-1.5 py-0.5 text-[11px] font-medium text-secondary">
              AnyPick proxy
            </span>
          ) : !isConfigured ? (
            <span className="flex items-center gap-1.5 text-[12px] text-secondary">
              <span className="size-1.5 rounded-full bg-amber-500" />
              Set Up…
            </span>
          ) : (
            <span className="text-[12px] text-secondary">Direct</span>
          )}

          <RowMenu label={`Actions for ${app.client}`} testId={`app.more-${app.clientId}`}>
            {(close) => (
              <>
                <MenuItem
                  testId="app.connection.change-menu"
                  onSelect={() => {
                    close();
                    onChangeConnection();
                  }}
                >
                  Change Connection…
                </MenuItem>
                <MenuItem
                  testId="apps-refresh-menu"
                  disabled={busy.size > 0 || !refresh}
                  onSelect={() => {
                    close();
                    setRefreshOpen((current) => !current);
                    if (refresh) service.mutate(refresh.operation, refresh.extra);
                  }}
                >
                  Refresh Models
                </MenuItem>
                <MenuDivider />
                <MenuItem
                  testId="app.disconnect-menu"
                  destructive
                  disabled={busy.size > 0 || !app.managed}
                  onSelect={() => {
                    close();
                    onReset();
                  }}
                >
                  Disconnect from AnyPick
                </MenuItem>
                <MenuItem
                  testId="app.remove-menu"
                  destructive
                  disabled={busy.size > 0}
                  onSelect={() => {
                    close();
                    onRemove();
                  }}
                >
                  Remove App
                </MenuItem>
              </>
            )}
          </RowMenu>

          <svg
            viewBox="0 0 24 24"
            width="12"
            height="12"
            aria-hidden="true"
            focusable="false"
            className="text-tertiary"
          >
            <path
              d="M9 5l7 7-7 7"
              fill="none"
              stroke="currentColor"
              strokeWidth="2.5"
              strokeLinecap="round"
              strokeLinejoin="round"
            />
          </svg>
        </RowActions>
      </Row>
      {refreshOpen ? (
        <Row testId={`apps-refresh-note-${app.clientId}`}>
          <RowText detail="Model list refreshed for the login this app is using." />
        </Row>
      ) : null}
    </>
  );
}

function uniqueClientIds(actions: Snapshot['actions']): string[] {
  return [...new Set(actions.map((action) => action.clientId).filter(Boolean))];
}

function clientName(actions: Snapshot['actions'], clientId: string): string {
  return actions.find((action) => action.clientId === clientId)?.client ?? clientId;
}
