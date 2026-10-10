import { useEffect, useMemo, useState } from 'react';
import { Symbol } from '../shell/Symbol';

import type { HubConflict, HubSource, ProxyRow, Snapshot } from '../lib/contract';
import { hubSourceChange } from '../lib/desktop-actions';
import {
  catalogModelCount,
  conflictDisplayInfo,
  hubProxy,
  hubServiceFooter,
  hubSourceDetail,
  hubSourceRows,
  hubStatusSubtitle,
  isGhost,
  publishedSummary,
  uniqueProviderCandidates,
  type HubRotationRow,
} from '../lib/hub';
import { parseModelIdentity } from '../lib/published';
import type { ServicePort } from '../lib/service';
import type { ServiceResult } from '../lib/wire';
import { ProviderIcon } from '../shell/ProviderIcon';
import { MenuItem, RowMenu } from '../ui/RowMenu';
import { ConfirmDialog } from './ProxiesPane';
import { Dialog } from '../ui/Dialog';
import {
  Button,
  CopyButton,
  EmptyState,
  Group,
  IconButton,
  Pane,
  Row,
  RowActions,
  RowText,
  Select,
  StatusLabel,
  Switch,
  useCopyConfirmation,
  WarningGlyph,
} from '../ui/primitives';
import { SegmentedControl } from '../ui/SegmentedControl';

/** The engine's refusal of a hub source toggle, and which row sent it. */
export type HubRowResult = { operation: string; result: ServiceResult };

/**
 * Proxy Hub: the service, its sources, and what it publishes.
 *
 * The service is a button, not a switch. The README puts lifecycle in the
 * Service row because a hub is started and stopped; a switch said "policy",
 * and the pane this replaced had neither the button nor the address.
 *
 * Every number here comes from the snapshot. macOS compiled its own routing
 * conflicts and published-model list from the catalogs; the Tauri app reads the
 * same fields the engine already publishes, so a conflict resolved in macOS
 * disappears here without a second compiler.
 */
export function ProxyHubPane({
  service,
  snapshot,
  query = '',
  busy = new Set<string>(),
  copyKeyResult = null,
  hubRowResult = null,
  onNavigate,
  onOpenModels,
}: {
  service: ServicePort;
  snapshot: Snapshot;
  query?: string;
  busy?: ReadonlySet<string>;
  /** Completion of a protected API-key copy request. */
  copyKeyResult?: { requestId: string; success: boolean | null } | null;
  /** The last refused source toggle, so the row that sent it can say so. */
  hubRowResult?: HubRowResult | null;
  onNavigate?: (destination: 'accounts' | 'proxies') => void;
  /** Opens Published Models, which the hub's own row leads to on macOS. */
  onOpenModels?: () => void;
}) {
  const [copyRequestId, setCopyRequestId] = useState<string | null>(null);
  const copyConfirmation = useCopyConfirmation();
  useEffect(() => {
    if (!copyRequestId) return;
    if (copyKeyResult?.requestId === copyRequestId) {
      if (copyKeyResult.success === null) return;
      copyConfirmation.report(copyKeyResult.success);
      setCopyRequestId(null);
      return;
    }
    if (!busy.has(copyRequestId)) {
      copyConfirmation.report(false);
      setCopyRequestId(null);
    }
  }, [busy, copyKeyResult, copyConfirmation.report, copyRequestId]);

  const needle = query.trim().toLocaleLowerCase();
  const hub = hubProxy(snapshot.proxies);
  const rows = hubSourceRows(snapshot.hubSources);

  const sources = rows.accounts.filter((source) => {
    const haystack = `${source.label} ${source.detail} ${source.name}`.toLocaleLowerCase();
    return !needle || haystack.includes(needle);
  });
  const rotations = rows.rotation.filter((source) => {
    const haystack = `${source.label} ${source.detail}`.toLocaleLowerCase();
    return !needle || haystack.includes(needle);
  });

  const enabledSources = rows.accounts.filter((source) => source.enabled && !isGhost(source));
  const actionable = snapshot.hubConflicts.filter(
    (conflict) => uniqueProviderCandidates(conflict).length >= 2,
  );
  const published = publishedSummary(snapshot.hubPublishedModels, enabledSources.length);

  const footer = hubServiceFooter({
    running: Boolean(hub?.running),
    enabled: Boolean(hub?.enabled),
    sourceCount: enabledSources.length,
    publishedCount: snapshot.hubPublishedModels.length,
    conflictCount: actionable.length,
  });

  const [pendingRotate, setPendingRotate] = useState(false);

  return (
    <Pane title="Proxy Hub" testId="proxy-hub">
      {hub ? (
        <Group title="Service" footer={footer} testId="hub.service">
          <Row testId="hub-service-row">
            {/* The same shape as a per-account relay row
                (`ProxyHubView.swift:23`): identity, the service's state in the
                subtitle, one trailing lifecycle button. This row carried a
                status dot *and* a subtitle saying "Running", plus Restart and
                Rotate — four controls where macOS has one and a context menu.
                Rotate also has a home already: the API Key row below. */}
            <ProviderIcon
              provider="proxy-hub"
              fallbackSymbol="point.3.connected.trianglepath.dotted"
              size={22}
              className="mr-1 text-secondary"
            />
            <RowText title={hub.label || 'Proxy Hub'} detail={hubStatusSubtitle(hub)} />
            <RowActions>
              {hub.running ? (
                <RowMenu label={`Actions for ${hub.label || 'Proxy Hub'}`} testId="hub.more">
                  {(close) => (
                    <MenuItem
                      testId="hub.restart"
                      disabled={!hub.restartActionId || busy.size > 0 || !hub.enabled}
                      onSelect={() => {
                        close();
                        service.invokeAction(hub.restartActionId ?? '', snapshot.revision);
                      }}
                    >
                      Restart Hub
                    </MenuItem>
                  )}
                </RowMenu>
              ) : null}
              <Button
                kind={hub.running ? 'destructive' : 'secondary'}
                testId="hub.service-actions"
                disabled={!hub.toggleActionId || busy.size > 0}
                ariaLabel={`${hub.running ? 'Stop' : 'Start'} ${hub.label || 'Proxy Hub'}`}
                onClick={() => service.invokeAction(hub.toggleActionId ?? '', snapshot.revision)}
              >
                {hub.running ? 'Stop' : 'Start'}
              </Button>
            </RowActions>
          </Row>

          <Row testId="hub.copy-address">
            <RowText title="Address" detail={hub.address ?? 'Not running'} />
            <RowActions>
              {hub.address ? (
                <CopyButton
                  label="Copy Proxy Hub address"
                  testId="hub.copy-address-button"
                  value={hub.address ?? ''}
                  copy={service.copy}
                />
              ) : null}
            </RowActions>
          </Row>

          {hub.hasToken ? (
            <Row testId="hub.copy-api-key">
              <RowText
                title="API Key"
                detail="Hidden until copied. The key never enters the snapshot (ADR 0006)."
              />
              <RowActions>
                <IconButton
                  label="Copy Proxy Hub API key"
                  testId="hub.copy-api-key-button"
                  phase={copyConfirmation.phase}
                  confirmed={<Symbol name="checkmark" size={13} />}
                  disabled={busy.size > 0}
                  onClick={() => {
                    if (!copyConfirmation.begin()) return;
                    setCopyRequestId(copyHubKey(service, hub));
                  }}
                >
                  <Symbol name="doc.on.doc" size={15} />
                </IconButton>
                <IconButton
                  label="Rotate Proxy Hub API key"
                  testId="hub-rotate-api-key"
                  onClick={() => setPendingRotate(true)}
                >
                  <svg viewBox="0 0 24 24" width="15" height="15" aria-hidden="true" focusable="false">
                    <path
                      d="M4 12a8 8 0 0 1 14-5 M20 12a8 8 0 0 1-14 5 M18 3v4h-4 M6 21v-4h4"
                      fill="none"
                      stroke="currentColor"
                      strokeWidth="1.6"
                      strokeLinecap="round"
                      strokeLinejoin="round"
                    />
                  </svg>
                </IconButton>
              </RowActions>
            </Row>
          ) : null}

          <Row
            testId="hub.models"
            onClick={snapshot.hubPublishedModels.length > 0 ? () => onOpenModels?.() : undefined}
          >
            <RowText title="Published Models" />
            <RowActions>
              <span className="text-[12px] whitespace-nowrap text-secondary">
                {published.summary}
                {published.needsChoice > 0 ? (
                  <span className="ml-1 text-[11px] text-tertiary">
                    · {published.needsChoice} need choice
                  </span>
                ) : null}
              </span>
              <button
                type="button"
                className="inline-flex size-5 cursor-default items-center justify-center text-tertiary hover:not-disabled:text-text disabled:opacity-30"
                data-testid="hub-open-models"
                aria-label="View Published Models"
                title="View models published by Proxy Hub"
                disabled={snapshot.hubPublishedModels.length === 0}
                onClick={(e) => {
                  e.stopPropagation();
                  onOpenModels?.();
                }}
              >
                <svg
                  viewBox="0 0 24 24"
                  width="13"
                  height="13"
                  fill="none"
                  stroke="currentColor"
                  strokeWidth="2.2"
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  aria-hidden="true"
                  focusable="false"
                >
                  <path d="m9 18 6-6-6-6" />
                </svg>
              </button>
            </RowActions>
          </Row>
        </Group>
      ) : (
        <EmptyState
          title="Proxy Hub Unavailable"
          detail="The engine published no hub for this machine, so there is nothing to start."
          testId="proxy-hub-empty"
        />
      )}

      {actionable.length > 0 ? (
        <Group
          title="Routing Conflicts"
          footer="Two accounts can serve the same model. Choose which one owns it before this hub serves that model."
          testId="hub-conflicts"
        >
          {actionable.map((conflict) => (
            <ConflictRow
              key={conflict.id}
              conflict={conflict}
              revision={snapshot.revision}
              service={service}
              busy={busy}
            />
          ))}
        </Group>
      ) : null}

      {rotations.length > 0 ? (
        <Group
          title="Request Strategy"
          footer="Failover sends every request to the owning account. Round Robin spreads them across the accounts that can serve the model."
          testId="hub-strategy"
        >
          {rotations.map((rotation) => (
            <StrategyRow
              key={rotation.id}
              rotation={rotation}
              service={service}
              busy={busy}
              error={rowError(hubRowResult, `hub.strategy.${rotation.hub}`)}
            />
          ))}
        </Group>
      ) : null}

      {sources.length === 0 && rotations.length === 0 ? (
        <EmptyState
          title={needle ? 'No Results' : 'No Hub Sources'}
          detail={
            needle
              ? 'No sources match your search.'
              : 'Save a login under Accounts & APIs, then switch it on here.'
          }
          actionTitle={needle ? undefined : 'Open Accounts & APIs'}
          onAction={needle ? undefined : () => onNavigate?.('accounts')}
          testId="proxy-hub-no-sources"
        />
      ) : sources.length > 0 ? (
        <Group
          title={`Accounts in This Hub (${enabledSources.length} of ${rows.accounts.length} enabled)`}
          footer="Switch on a saved login to publish the models it can serve. Account proxies under Proxies are separate and stay running on their own addresses."
          testId="proxy-hub-sources"
        >
          {sources.map((source) => (
            <SourceRow
              key={source.id}
              source={source}
              catalogs={snapshot.hubCatalogs}
              service={service}
              busy={busy}
              error={rowError(hubRowResult, `hub.account.${source.id}`)}
            />
          ))}
        </Group>
      ) : null}

      {pendingRotate && hub?.rotateActionId ? (
        <ConfirmDialog
          title="Rotate Proxy Hub API Key?"
          message="Apps configured with the current key stop connecting until they are updated. This cannot be undone."
          confirmTitle="Rotate Key"
          destructive
          testId="hub-rotate-dialog"
          onCancel={() => setPendingRotate(false)}
          onConfirm={() => {
            setPendingRotate(false);
            service.invokeAction(hub.rotateActionId ?? '', snapshot.revision);
          }}
        />
      ) : null}
    </Pane>
  );
}

/**
 * A hub proxy row has no `status` field of its own: it is the engine's own
 * liveness check against the hub's health echo, published as `running`. A hub
 * that is enabled but not answering is the one case worth a second colour,
 * because Start is the action that fixes it.
 */
function hubStatus(hub: ProxyRow): 'running' | 'attention' | 'stopped' {
  if (hub.running) return 'running';
  return hub.enabled ? 'attention' : 'stopped';
}

function copyHubKey(service: ServicePort, hub: ProxyRow): string {
  // The engine mints the key on request and returns it in the result payload.
  // Nothing here reads or stores it; App copies it and names the action only.
  return service.mutate('proxy-token', { providerId: hub.providerId, name: hub.id.split('/').slice(1).join('/') });
}

/**
 * The refusal for one row, if the last answer belongs to it.
 *
 * A banner would say something failed without saying which switch is now in a
 * state the user did not choose. Each row's id is the row key, so an answer for
 * a source the user cannot see is not shown on one they can.
 */
function rowError(result: HubRowResult | null, rowKey: string): string | undefined {
  if (!result || result.result.status !== 'error') return undefined;
  return result.result.message || undefined;
}

function SourceRow({
  source,
  catalogs,
  service,
  busy,
  error,
}: {
  source: HubSource;
  catalogs: Snapshot['hubCatalogs'];
  service: ServicePort;
  busy: ReadonlySet<string>;
  error?: string;
}) {
  const ghost = isGhost(source);
  const detail = hubSourceDetail(source, catalogs);
  const models = catalogModelCount(catalogs, source.id);

  return (
    <Row warning={ghost} testId={`hub.account.${source.id}`}>
      <RowText
        title={source.label || source.name}
        detail={detail}
        issue={ghost ? source.unavailableReason : undefined}
      />
      {error ? (
        <span className="flex items-center gap-1 text-[12px] text-orange" data-testid={`hub.account.${source.id}.error`}>
          <WarningGlyph />
          {error}
        </span>
      ) : null}
      <RowActions>
        {models > 0 ? <span className="text-[12px] whitespace-nowrap text-secondary">{models}</span> : null}
        {ghost ? (
          <span className="text-[12px] whitespace-nowrap text-secondary">Unavailable</span>
        ) : (
          <Switch
            checked={source.enabled}
            disabled={busy.size > 0}
            label={`${source.enabled ? 'Disable' : 'Enable'} ${source.label || source.name}`}
            onChange={(enabled) => {
              const action = hubSourceChange({ hub: source.hub, source, enabled });
              service.mutate(action.operation, action.extra);
            }}
          />
        )}
      </RowActions>
    </Row>
  );
}

function StrategyRow({
  rotation,
  service,
  busy,
  error,
}: {
  rotation: HubRotationRow;
  service: ServicePort;
  busy: ReadonlySet<string>;
  error?: string;
}) {
  const count = 'accountCount' in rotation ? (rotation.accountCount as number) : 0;
  return (
    <Row testId={`hub.strategy.${rotation.hub}`} warning={count < 2}>
      <RowText
        title="Strategy"
        issue={count < 2 ? 'Needs two or more accounts in this hub.' : undefined}
      />
      {error ? (
        <span
          className="flex items-center gap-1 text-[12px] text-orange"
          data-testid={`hub.strategy.${rotation.hub}.error`}
        >
          <WarningGlyph />
          {error}
        </span>
      ) : null}
      <RowActions>
        <SegmentedControl
          value={rotation.enabled ? 'round-robin' : 'failover'}
          testId={`hub.strategy.${rotation.hub}`}
          disabled={busy.size > 0 || count < 2}
          options={[
            { value: 'failover', label: 'Failover' },
            { value: 'round-robin', label: 'Round Robin' },
          ]}
          onChange={(value) => {
            const action = hubSourceChange({
              hub: rotation.hub,
              source: rotation,
              enabled: value === 'round-robin',
            });
            service.mutate(action.operation, action.extra);
          }}
        />
      </RowActions>
    </Row>
  );
}

function ConflictRow({
  conflict,
  revision,
  service,
  busy,
}: {
  conflict: HubConflict;
  revision: number;
  service: ServicePort;
  busy: ReadonlySet<string>;
}) {
  const candidates = uniqueProviderCandidates(conflict);
  const info = useMemo(() => conflictDisplayInfo(conflict), [conflict]);
  const busyHere = busy.size > 0;
  const conflictModelId = conflict.models?.[0] ?? conflict.title;
  const parsed = useMemo(
    () => parseModelIdentity(conflictModelId, conflict.candidates?.[0]?.providerId ?? ''),
    [conflictModelId, conflict.candidates],
  );

  return (
    <Row testId={`hub.conflict.${conflict.id}`}>
      <ProviderIcon provider={parsed.vendorKey} fallbackSymbol="cube.transparent" size={20} className="mr-1 text-secondary" />
      <RowText title={<span className="font-mono text-[12px]">{info.title}</span>} detail={info.subtitle} />
      <RowActions>
        <Select
          label=""
          testId={`hub.conflict.${conflict.id}.picker`}
          disabled={busyHere || candidates.length === 0}
          value=""
          onChange={(actionId) => {
            // A select that emits its own value as a placeholder would send
            // "Choose provider…" to the engine. The empty value is the
            // unchosen state, so an empty change is ignored instead.
            if (!actionId) return;
            service.invokeAction(actionId, revision);
          }}
          options={[
            { value: '', label: 'Select Provider…' },
            ...candidates.map((candidate) => ({ value: candidate.actionId, label: candidate.label })),
          ]}
        />
      </RowActions>
    </Row>
  );
}

