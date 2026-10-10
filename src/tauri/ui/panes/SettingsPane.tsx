import { useEffect, useState } from 'react';

import type { Preferences } from '../lib/contract';
import {
  SETTING_ROWS,
  settingChange,
} from '../lib/desktop-actions';
import type { ServicePort } from '../lib/service';
import { Button, Group, Pane, Row, RowActions, RowText, Switch } from '../ui/primitives';

/**
 * App-wide preferences as one regular content pane. Settings is a UI route in
 * the app shell, not an engine destination, so the data protocol remains
 * limited to the seven engine-backed views.
 */
export function SettingsPane({
  settings,
  service,
  busy = false,
  onShowGettingStarted,
}: {
  settings: Preferences;
  service: ServicePort;
  busy?: boolean;
  onShowGettingStarted: () => void;
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

  return (
    <Pane title="Settings" testId="settings-pane">
      <Group title="Startup" testId="settings-startup">
        {startup.map((row) => (
          <Row key={row.operation} testId={`setting-${row.key}`}>
            <RowText
              title={row.label}
              detail={row.key === 'launchAtLogin'
                ? 'Start AnyPick when you sign in.'
                : 'Apps can also start a needed proxy later.'}
            />
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
        title="Usage"
        testId="settings-usage"
      >
        {usage.map((row) => (
          <Row key={row.operation} testId={`setting-${row.key}`}>
            <RowText
              title={row.label}
              detail={row.key === 'showQuota'
                ? 'Shows usage reported for the account active in its provider app.'
                : 'For pooled proxies only. After a confirmed limit, AnyPick cools down that account so another can handle the request.'}
            />
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

      <Group title="Getting Started" testId="settings-getting-started">
        <Row>
          <RowText title="Setup Checklist" detail="Show the checklist on Overview again." />
          <RowActions>
            <Button testId="settings.show-getting-started" onClick={onShowGettingStarted}>
              Show Checklist
            </Button>
          </RowActions>
        </Row>
      </Group>
    </Pane>
  );
}
