import { useState } from 'react';

import type { Destination, Snapshot } from '../lib/contract';
import {
  isOnboardingCoreComplete,
  onboardingFooter,
  overviewApps,
  overviewConnectionSummary,
  overviewTransportLabel,
} from '../lib/overview';
import {
  attentionCopy,
  disambiguatedProxyNames,
  overviewAttentionTarget,
  overviewServiceTotal,
  proxyStatus,
  STATUS_LABEL,
} from '../lib/model';
import { Chevron } from '../ui/Chevron';
import { ProviderIcon } from '../shell/ProviderIcon';
import { Symbol } from '../shell/Symbol';
import { Button, Group, Pane, PaneActions, Row, RowActions, RowText, WarningGlyph } from '../ui/primitives';

const ONBOARDING_KEY = 'anypick.onboardingDismissed';

/**
 * Overview: attention first, then the checklist, then a summary.
 *
 * The pane's job is to name the single most useful next action, not to
 * summarize everything. `overviewAttentionTarget` picks that one thing in a
 * fixed order, so the call to action never contradicts itself between
 * refreshes.
 *
 * It takes no service port: nothing on this pane mutates anything, so there is
 * nothing for it to ask the engine.
 */
export function OverviewPane({
  snapshot,
  onNavigate,
}: {
  snapshot: Snapshot;
  onNavigate?: (destination: Destination) => void;
}) {
  const [onboardingDismissed, setOnboardingDismissed] = useState(
    () => globalThis.localStorage?.getItem(ONBOARDING_KEY) === '1',
  );
  const apps = overviewApps(snapshot.actions);
  const hub = snapshot.proxies.find((proxy) => proxy.providerId === 'proxy-hub');
  const accountProxies = snapshot.proxies.filter((proxy) => proxy.providerId !== 'proxy-hub');
  const target = overviewAttentionTarget({
    proxies: accountProxies,
    apps: snapshot.actions,
    accounts: snapshot.accounts,
    conflicts: snapshot.hubConflicts,
  });

  const running = accountProxies.filter((proxy) => proxy.running).length;
  // A hub with no sources and no enablement is a phantom row; counting it
  // would tell a user a service is running that is not.
  const total = overviewServiceTotal(
    snapshot.proxyCount,
    snapshot.hubSources.length === 0,
    Boolean(hub?.running),
  );

  // Named the same way the Proxies pane names it, so the two panes never point
  // at different things with the same words.
  const proxyNames = disambiguatedProxyNames(accountProxies);

  const attention = target
    ? attentionCopy(target, {
        proxyName: target.kind === 'proxy' ? proxyNames.get(target.id) : undefined,
        appName: target.kind === 'app'
          ? snapshot.actions.find((row) => row.clientId === target.id)?.client
          : undefined,
        accountName: target.kind === 'account'
          ? snapshot.accounts.find((row) => row.id === target.id)?.label
          : undefined,
        conflictTitle: target.kind === 'conflict'
          ? snapshot.hubConflicts.find((row) => row.id === target.id)?.title
          : undefined,
      })
    : null;

  const destinationFor = (action: string): Destination => {
    if (action.includes('Proxies')) return 'proxies';
    if (action.includes('Apps')) return 'apps';
    if (action.includes('Accounts')) return 'accounts';
    return 'proxyHub';
  };

  return (
    <Pane title="Overview" testId="overview">
      {snapshot.degradedConditions.length > 0 ? (
        <Group title="Needs Attention" testId="overview-degraded">
          {snapshot.degradedConditions.map((condition) => (
            <Row key={condition.id} warning testId={`degraded-${condition.id}`}>
              <WarningGlyph />
              <RowText title={condition.title} detail={condition.detail} />
            </Row>
          ))}
        </Group>
      ) : null}

      {attention ? (
        <Group testId="overview-attention">
          <Row warning testId="overview-attention-row">
            <WarningGlyph />
            <RowText title={attention.title} detail={attention.detail} />
            <RowActions>
              <Button onClick={() => onNavigate?.(destinationFor(attention.action))}>
                {attention.action}
              </Button>
            </RowActions>
          </Row>
        </Group>
      ) : null}

      {onboardingDismissed ? null : (
      <Group
        title="Get Started"
        footer={onboardingFooter(isOnboardingCoreComplete(snapshot))}
        testId="overview-get-started"
      >
        <Row testId="onboarding-save-login">
          <RowText
            title="Save a Login"
            detail={
              snapshot.accounts.length > 0
                ? 'A login is saved on this machine.'
                : 'Use a login already on this machine.'
            }
          />
          <RowActions>
            {snapshot.accounts.length > 0 ? (
              <span className="text-[12px] whitespace-nowrap text-secondary">Done</span>
            ) : (
              <Button onClick={() => onNavigate?.('accounts')}>Save…</Button>
            )}
          </RowActions>
        </Row>
        <Row testId="onboarding-choose-route">
          <RowText
            title="Point an App"
            detail={
              snapshot.actions.some((row) => row.selected)
                ? 'An app is using a saved login.'
                : 'Point Claude or Codex at a saved login.'
            }
          />
          <RowActions>
            <Button onClick={() => onNavigate?.('apps')}>Choose…</Button>
          </RowActions>
        </Row>
        <Row testId="onboarding-hub">
          <RowText
            title="Set Up Proxy Hub (Optional)"
            detail={
              snapshot.hubPublishedModels.length > 0
                ? 'Hub sources are ready.'
                : 'One local endpoint for many models. Skip it if unsure.'
            }
          />
          <RowActions>
            <Button onClick={() => onNavigate?.('proxyHub')}>Set Up…</Button>
          </RowActions>
        </Row>
        <PaneActions>
          <Button
            quiet
            testId="overview.hide-onboarding"
            onClick={() => {
              globalThis.localStorage?.setItem(ONBOARDING_KEY, '1');
              setOnboardingDismissed(true);
            }}
          >
            Hide Getting Started
          </Button>
        </PaneActions>
      </Group>
      )}

      {/*
        Three rows, then a way to the rest. Listing every app makes Overview a
        second Apps pane, and pushes the app that needs attention off the screen
        behind the ones that are already fine.
      */}
      {apps.listed.length > 0 ? (
        <Group title={apps.title} footer={apps.footer} testId="overview-apps">
          {apps.listed.map((app) => (
            <Row key={app.clientId} testId={`overview.app.${app.clientId}`}>
              {/* `DesignProviderIcon` (`OverviewView.swift:180`): the app's own
                  mark, so three rows of names read as three apps rather than
                  three more text lines. */}
              <ProviderIcon provider={app.clientId} size={24} className="mr-1" />
              <RowText
                title={app.client}
                detail={overviewConnectionSummary(app)}
                issue={app.needsAttention ? 'Needs a login before it can run.' : undefined}
              />
              <RowActions>
                <span
                  className={`text-[12px] whitespace-nowrap ${
                    app.needsAttention ? 'text-orange' : 'text-secondary'
                  }`}
                >
                  {overviewTransportLabel(app)}
                </span>
              </RowActions>
            </Row>
          ))}
          {apps.hasMore ? (
            <Row testId="overview.apps.see-all" onClick={() => onNavigate?.('apps')}>
              <RowText title={`All Apps (${apps.total})…`} />
              <RowActions>
                <Chevron />
              </RowActions>
            </Row>
          ) : null}
        </Group>
      ) : (
        <Group title="Apps" testId="overview-apps">
          <Row>
            <RowText detail="No app routes are available yet." />
          </Row>
        </Group>
      )}

      <Group title="Services" testId="overview-services">
        <Row testId="overview-services-row" onClick={() => onNavigate?.('proxies')}>
          <span
            className={`mr-2.5 flex-none ${
              running === 0 ? 'text-secondary' : 'text-accent'
            }`}
            aria-hidden="true"
          >
            <Symbol name="server.rack" size={18} />
          </span>
          <RowText
            title="Proxies"
            detail={
              running === 0
                ? 'Services are stopped.'
                : `${running} service${running === 1 ? '' : 's'} running`
            }
          />
          <RowActions>
            <div className="flex items-center gap-1.5">
              {running > 0 ? (
                <span className="inline-block size-[7px] rounded-full bg-green flex-none" />
              ) : null}
              <span className="text-[12px] whitespace-nowrap text-secondary font-mono">
                {running} of {total} running
              </span>
              <Chevron />
            </div>
          </RowActions>
        </Row>
      </Group>

      {snapshot.activity.length > 0 ? (
        <Group title="Recent Activity" testId="overview-activity">
          {snapshot.activity.slice(0, 3).map((event) => (
            <Row key={event.id} testId={`overview-activity-${event.id}`}>
              {/* macOS wears the event's provider mark at 18pt
                  (`OverviewView.swift:261`), and falls back to the status symbol
                  when the event names no provider. */}
              {event.provider ? (
                <ProviderIcon provider={event.provider} size={18} className="mr-1" />
              ) : (
                <span className="mr-1 flex-none text-secondary" aria-hidden="true">
                  <Symbol name="clock.arrow.circlepath" size={15} />
                </span>
              )}
              <RowText title={event.message} detail={event.kind} />
            </Row>
          ))}
        </Group>
      ) : null}

      {hub ? (
        <Group title="Proxy Hub" testId="overview-hub">
          <Row onClick={() => onNavigate?.('proxyHub')}>
            <span className="mr-2.5 flex-none text-accent" aria-hidden="true">
              <Symbol name="point.3.connected.trianglepath.dotted" size={18} />
            </span>
            <RowText title="Proxy Hub" detail={hub.address ?? hub.detail} />
            <RowActions>
              <div className="flex items-center gap-1.5">
                <span className="text-[12px] whitespace-nowrap text-secondary">{STATUS_LABEL[proxyStatus(hub)]}</span>
                <Chevron />
              </div>
            </RowActions>
          </Row>
        </Group>
      ) : null}
    </Pane>
  );
}

