import { useEffect, useState } from 'react';

import type { Destination, Preferences, Snapshot } from '../lib/contract';
import {
  SETTING_ROWS,
  missingCodexProfileSources,
  settingChange,
  syncCodexProfiles,
} from '../lib/desktop-actions';
import type { ServicePort } from '../lib/service';
import type { DoctorFrame } from '../lib/wire';
import { Dialog } from '../ui/Dialog';
import { Button, Group, Row, RowActions, RowText, StatusLabel, Switch, ValueText } from '../ui/primitives';

/** The one pref that is not engine state: whether the checklist shows again. */
const ONBOARDING_KEY = 'anypick.onboardingDismissed';

/**
 * The macOS Settings scene, opened beside the sidebar rather than as an eighth
 * destination. The seven sidebar rows stay the product navigation.
 *
 * macOS splits this across five panes behind a tab bar (General, Usage,
 * Proxies, Security, Diagnostics). A webview has no `TabView` and a modal
 * sheet cannot host one, so the panes stack into one scroll: the same rows,
 * grouped under the same headings, without inventing a tab control that macOS
 * would not let a dialog own.
 *
 * The panes this sheet was missing are the ones that only *navigate*: Open
 * Proxies, Open Account Backups, Show Getting Started. They are here because a
 * Settings pane whose commands cannot reach the panes they name is a dead end.
 */
export function SettingsSheet({
  settings,
  snapshot,
  service,
  busy = false,
  report = null,
  onClose,
  onNavigate,
  onShowGettingStarted,
}: {
  settings: Preferences;
  snapshot: Snapshot;
  service: ServicePort;
  busy?: boolean;
  report?: DoctorFrame | null;
  onClose: () => void;
  onNavigate?: (destination: Destination) => void;
  /** Un-hides the Overview checklist and goes there. */
  onShowGettingStarted?: () => void;
}) {
  const [pending, setPending] = useState<Partial<Record<(typeof SETTING_ROWS)[number]['key'], boolean>>>({});
  // A failed change does not publish a new snapshot. Dropping the optimistic
  // value when the request finishes puts the switch back on the last truth.
  useEffect(() => {
    if (!busy) setPending({});
  }, [busy, settings]);

  const toggle = (row: (typeof SETTING_ROWS)[number]) => (enabled: boolean) => {
    setPending((current) => ({ ...current, [row.key]: enabled }));
    const action = settingChange(row.operation, enabled);
    service.mutate(action.operation, action.extra);
  };

  const startup = SETTING_ROWS.filter((row) => row.key === 'launchAtLogin' || row.key === 'startEnabledProxies');
  const usage = SETTING_ROWS.filter((row) => row.key === 'showQuota' || row.key === 'quotaGuardEnabled');
  const missingCodex = missingCodexProfileSources(snapshot);
  const unreadyCodex = missingCodex.filter((sourceId) =>
    (snapshot.codexProfileSources.find((source) => source.sourceId === sourceId)?.models.length ?? 0) === 0,
  );

  return (
    <Dialog
      title="Settings"
      onCancel={onClose}
      testId="settings-sheet"
      width="form"
      actions={
        <>
          <Button kind="primary" onClick={onClose}>
            Close
          </Button>
        </>
      }
    >
      <Group title="Startup" footer="A proxy can still start when an app needs it." testId="settings-tabs">
        {startup.map((row) => (
          <Row key={row.operation} testId={`setting-${row.key}`}>
            <RowText title={row.label} />
            <RowActions>
              <Switch
                label={row.label}
                checked={pending[row.key] ?? settings[row.key]}
                disabled={busy}
                onChange={toggle(row)}
              />
            </RowActions>
          </Row>
        ))}
        <Row>
          <RowText title="Proxies" />
          <RowActions>
            <Button
              kind="secondary"
              testId="settings.open-proxies"
              onClick={() => {
                onClose();
                onNavigate?.('proxies');
              }}
            >
              Open Proxies…
            </Button>
          </RowActions>
        </Row>
      </Group>

      <Group title="Getting Started" footer="Shown on Overview until setup is finished.">
        <Row>
          <RowText title="Getting Started" />
          <RowActions>
            <Button
              kind="secondary"
              testId="settings.getting-started"
              onClick={() => {
                // Un-hiding is the command; navigating is what makes it
                // visible. macOS does both, in that order.
                try {
                  globalThis.localStorage?.removeItem(ONBOARDING_KEY);
                } catch {
                  // A pref that cannot be stored still applies for this run.
                }
                onShowGettingStarted?.();
                onClose();
                onNavigate?.('overview');
              }}
            >
              Show
            </Button>
          </RowActions>
        </Row>
      </Group>

      <Group
        title="Usage"
        footer="Quota Guard pauses a pooled proxy account after a confirmed usage limit. Native app logins are never switched automatically."
        testId="settings-usage"
      >
        {usage.map((row) => (
          <Row key={row.operation} testId={`setting-${row.key}`}>
            <RowText title={row.label} detail={row.key === 'showQuota' ? 'From the login currently signed in on this Mac.' : undefined} />
            <RowActions>
              <Switch
                label={row.label}
                checked={pending[row.key] ?? settings[row.key]}
                disabled={busy}
                onChange={toggle(row)}
              />
            </RowActions>
          </Row>
        ))}
      </Group>

      <Group
        title="Security"
        footer="API keys and proxy tokens never appear in the menu, activity, or logs. Proxies accept connections only from this Mac."
      >
        <Row>
          <RowText title="Hide Tokens" />
          <RowActions>
            <ValueText>On</ValueText>
          </RowActions>
        </Row>
        <Row>
          <RowText title="Local Access Only" />
          <RowActions>
            <ValueText>On</ValueText>
          </RowActions>
        </Row>
      </Group>

      <Group title="Backups" footer="Backup files include credentials.">
        <Row>
          <RowText title="Account Backups" />
          <RowActions>
            <ValueText>Unencrypted</ValueText>
          </RowActions>
        </Row>
        <Row>
          <RowText title="Review Backups" />
          <RowActions>
            <Button
              kind="secondary"
              testId="settings.review-backups"
              onClick={() => {
                onClose();
                onNavigate?.('backups');
              }}
            >
              Open Account Backups…
            </Button>
          </RowActions>
        </Row>
      </Group>

      <Group
        title="Codex Profiles"
        footer="Profiles can be created when their account model list is available. Refresh accounts above, then create the remaining profiles here."
        testId="settings-codex-profiles"
      >
        {missingCodex.length === 0 ? (
          <Row>
            <RowText title="Profiles" detail="All compatible accounts have a profile." />
          </Row>
        ) : (
          <>
            <Row>
              <RowText title="Missing Profiles" />
              <RowActions>
                <ValueText>{String(missingCodex.length)}</ValueText>
              </RowActions>
            </Row>
            {unreadyCodex.map((sourceId) => (
              <Row key={sourceId} testId={`settings.codex-unready-${sourceId}`}>
                <RowText
                  title={snapshot.codexProfileSources.find((source) => source.sourceId === sourceId)?.label ?? sourceId}
                  detail="Model list unavailable"
                />
                <RowActions>
                  <Button
                    kind="secondary"
                    disabled={busy}
                    onClick={() => {
                      const action = settingChange('account-refresh', true);
                      void action;
                      service.mutate('account-refresh', {
                        providerId: sourceId.split('/')[1] ?? sourceId,
                        name: sourceId.split('/').slice(2).join('/'),
                      });
                    }}
                  >
                    Refresh Models
                  </Button>
                </RowActions>
              </Row>
            ))}
            <Row>
              <RowActions>
                <Button
                  kind="secondary"
                  testId="settings.create-codex-profiles"
                  disabled={busy || missingCodex.length === unreadyCodex.length}
                  onClick={() => {
                    const action = syncCodexProfiles(missingCodex);
                    service.mutate(action.operation, action.extra);
                  }}
                >
                  Create Missing Profiles…
                </Button>
              </RowActions>
            </Row>
          </>
        )}
        {unreadyCodex.length > 0 ? (
          <Row>
            <RowActions>
              <Button
                kind="secondary"
                disabled={busy}
                onClick={() => {
                  onClose();
                  onNavigate?.('accounts');
                }}
              >
                Open Accounts & APIs…
              </Button>
            </RowActions>
          </Row>
        ) : null}
      </Group>

      <Group
        title="Installation Health"
        footer="Doctor checks saved account references, proxies, routing, plugins, and proxy environment settings. It does not change data."
        testId="settings-diagnostics"
      >
        <Row testId="settings-doctor-status">
          <RowText title="Status" />
          <RowActions>
            {report === null ? (
              <span className="text-[12px] whitespace-nowrap text-secondary">Not checked</span>
            ) : (
              <StatusLabel status={report.ok ? 'ready' : 'attention'} />
            )}
          </RowActions>
        </Row>
        {report?.findings.map((finding) => (
          <Row key={finding.check} testId={`settings-doctor-${finding.check}`}>
            <RowText title={finding.check} detail={finding.detail} />
          </Row>
        ))}
        <Row>
          <RowActions>
            <Button
              kind="secondary"
              testId="settings-doctor"
              disabled={busy}
              onClick={() => service.requestDoctor()}
            >
              Run Doctor
            </Button>
          </RowActions>
        </Row>
      </Group>
    </Dialog>
  );
}