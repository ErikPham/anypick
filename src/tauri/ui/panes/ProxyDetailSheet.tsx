import { useState } from 'react';

import type { ProxyRow, Snapshot } from '../lib/contract';
import {
  proxyClientApps,
  proxyFailure,
  proxyOperation,
  proxyPort,
  proxyPurpose,
  servingAccountLabel,
} from '../lib/proxy-detail';
import { canRestartProxy, nativeProviderName, proxyStatus, proxyTokenSubject } from '../lib/model';
import type { ServicePort } from '../lib/service';
import { ProviderIcon } from '../shell/ProviderIcon';
import { ConfirmDialog } from './ProxiesPane';
import { DisclosureChevron } from '../ui/Chevron';
import { Dialog } from '../ui/Dialog';
import {
  Button,
  Group,
  IconButton,
  Row,
  RowActions,
  RowText,
  StatusLabel,
  WarningRow,
} from '../ui/primitives';

/**
 * `ProxyDetailView`. One relay's identity, its endpoint, and who is using it.
 *
 * macOS pushes this onto the detail stack; a webview has no stack, so it is a
 * dialog the Proxies row opens — the same trade `AccountDetailSheet` makes.
 *
 * This is the destination that makes the Proxies pane's own footer true: *"Select
 * a proxy to view its address and API key."* Before it existed the address sat
 * on the list row and the footer was a lie.
 */
export function ProxyDetailSheet({
  proxy,
  snapshot,
  service,
  busy = new Set<string>(),
  revision,
  result = null,
  onNavigate,
  onClose,
}: {
  proxy: ProxyRow;
  snapshot: Snapshot;
  service: ServicePort;
  busy?: ReadonlySet<string>;
  revision: number;
  /** The engine's answer when the last answer belonged to this relay. */
  result?: { status: string; message: string } | null;
  onNavigate?: (destination: 'activity') => void;
  onClose: () => void;
}) {
  const [confirming, setConfirming] = useState<'stop' | 'rotate' | null>(null);

  const status = proxyStatus(proxy);
  const port = proxyPort(proxy);
  const running = proxy.running;
  const operation = proxyOperation(proxy, service.inFlight());
  const failure = proxyFailure(proxy, snapshot.degradedConditions);
  const apps = proxyClientApps(proxy, snapshot.actions);
  const idle = busy.size === 0;
  const refused = result && result.status !== 'success' ? result : undefined;

  return (
    <Dialog
      title={proxy.label || 'Proxy'}
      onCancel={onClose}
      testId="proxies-inspector"
      width="form"
      actions={
        <>
          {/* `proxyActions` (`ProxyDetailView.swift:252`): Restart is the pane
              action, trailing, and disabled unless the relay could actually
              restart. */}
          <Button
            kind="secondary"
            testId="proxies.inspector.restart"
            disabled={!idle || !running || !canRestartProxy(running, status) || !proxy.restartActionId}
            onClick={() => service.invokeAction(proxy.restartActionId ?? '', revision)}
          >
            {operation?.verb === 'Restarting' ? 'Restarting…' : 'Restart Proxy'}
          </Button>
          <Button kind="primary" onClick={onClose}>
            Done
          </Button>
        </>
      }
    >
      <Group title="Service" testId="proxies-inspector.identity">
        <Row>
          <ProviderIcon provider={proxy.providerId} fallbackSymbol="server.rack" size={26} className="mr-1" />
          <RowText title={proxy.label} detail={proxyPurpose(proxy)} />
        </Row>
        <Row testId="proxies-inspector.status">
          <RowText title="Status" />
          <RowActions>
            {/*
              `ProgressView` + the operation word while a command is in flight,
              and the status label otherwise. macOS reads the two from
              `serviceOperations[proxy.id]` and `proxy.status`, and HIG is
              explicit that the lifecycle button keeps its label and spins inside
              it rather than being swapped for a bare spinner.
            */}
            {operation ? (
              <span className="inline-flex items-center gap-1.5 text-[12px] text-secondary">
                <Spinner />
                {operation.verb}
              </span>
            ) : (
              <StatusLabel status={status} />
            )}
            <Button
              kind={running ? 'destructive' : 'secondary'}
              testId="proxies.inspector.lifecycle"
              disabled={!idle || !proxy.toggleActionId}
              ariaLabel={`${running ? 'Stop' : 'Start'} ${proxy.label}`}
              onClick={() =>
                running ? setConfirming('stop') : service.invokeAction(proxy.toggleActionId ?? '', revision)
              }
            >
              {running ? 'Stop' : 'Start'}
            </Button>
          </RowActions>
        </Row>
        <Row testId="proxies-inspector.provider">
          <RowText title="Provider" />
          <RowActions>
            <span className="text-[12px] whitespace-nowrap text-secondary">
              {nativeProviderName(proxy.providerId)}
            </span>
          </RowActions>
        </Row>
        {servingAccountLabel(proxy, snapshot.accounts) ? (
          <Row testId="proxies-inspector.account">
            <RowText title="Serving Account" />
            <RowActions>
              <span className="text-[12px] whitespace-nowrap text-secondary">
                {servingAccountLabel(proxy, snapshot.accounts)}
              </span>
            </RowActions>
          </Row>
        ) : null}
      </Group>

      {/*
        A failure and a stopped-but-enabled relay are different statements, and
        macOS gives them different advice (`ProxyDetailView.swift:66`, `:81`):
        one says fix the configuration, the other says press Start.
      */}
      {failure ? (
        <Group testId="proxies-inspector.failure">
          <WarningRow title="Service Error" detail={failure} />
        </Group>
      ) : proxy.enabled && !running ? (
        <Group testId="proxies-inspector.attention">
          <WarningRow
            title="Needs Attention"
            detail="This proxy is enabled but not currently running."
          />
        </Group>
      ) : null}

      {/*
        A refused command belongs to the row that sent it. A banner over the pane
        would say something failed without saying which relay is now in a state
        the user did not choose — the same rule the hub source rows follow.
      */}
      {refused ? (
        <Group testId="proxies-inspector.error">
          <WarningRow title="Command Refused" detail={refused.message} />
        </Group>
      ) : null}

      <Group title="Endpoint" testId="proxies-inspector.endpoint">
        <Row testId="proxies.inspector.address-row">
          <RowText title="Address" />
          <RowActions>
            {port > 0 ? (
              <>
                {/* Monospaced and selectable, as on macOS: this is a value to
                    paste into a client's config, not a sentence. */}
                <span className="select-all font-mono text-[12px] text-secondary">
                  http://127.0.0.1:{port}
                </span>
                <IconButton
                  label="Copy proxy address"
                  testId="proxies.inspector.copy"
                  onClick={() => void service.copy(`http://127.0.0.1:${port}`)}
                >
                  <CopyGlyph />
                </IconButton>
              </>
            ) : (
              <span className="text-[12px] whitespace-nowrap text-secondary" data-testid="proxies.inspector.address">
                Not running
              </span>
            )}
          </RowActions>
        </Row>
        {port > 0 && proxy.hasToken ? (
          <Row testId="proxies.inspector.api-key">
            <RowText title="API Key" />
            <RowActions>
              {/*
                ADR 0006: the snapshot says a copy is possible, never the value.
                The bearer arrives in a result payload, on request, so this row
                can only ever say that.
              */}
              <span className="text-[12px] whitespace-nowrap text-secondary">Hidden until copied</span>
              <IconButton
                label="Copy proxy API key"
                testId="proxies.inspector.copy-key"
                onClick={() => {
                  const subject = proxyTokenSubject(proxy.id, proxy.providerId);
                  if (subject) service.mutate('proxy-token', subject);
                }}
              >
                <CopyGlyph />
              </IconButton>
              {running ? (
                <IconButton
                  label="Rotate API Key"
                  testId="proxies.inspector.rotate-key"
                  onClick={() => setConfirming('rotate')}
                >
                  <RotateGlyph />
                </IconButton>
              ) : null}
            </RowActions>
          </Row>
        ) : null}
        {port > 0 ? (
          <Row testId="proxies-inspector.port">
            <RowText title="Port" />
            <RowActions>
              <span className="text-[12px] whitespace-nowrap text-secondary">{port}</span>
            </RowActions>
          </Row>
        ) : null}
        <Row testId="proxies-inspector.security">
          <RowText title="Security" />
          <RowActions>
            <span className="text-[12px] whitespace-nowrap text-secondary">
              Loopback only (authenticated)
            </span>
          </RowActions>
        </Row>
        <Row testId="proxies-inspector.activity">
          <RowText title="Activity & Logs" />
          <RowActions>
            {/* A destination, so it wears a chevron the way a `NavigationLink`
                does rather than sitting in the pane's action row. */}
            <button
              type="button"
              className="inline-flex cursor-default items-center gap-1 border-0 bg-transparent text-[12px] text-accent"
              onClick={() => onNavigate?.('activity')}
            >
              View in Activity
              <DisclosureChevron />
            </button>
          </RowActions>
        </Row>
      </Group>

      {/*
        The reason this destination exists and a list row does not: which apps
        you will disconnect if you stop this relay. Stopping is confirmed, and
        the confirmation cannot name the apps from a menu item's context.
      */}
      {apps.length > 0 ? (
        <Group title="Client Apps" testId="proxies-inspector.apps">
          {apps.map((app) => (
            <Row key={app.clientId} testId={`proxies-inspector.app.${app.clientId}`}>
              <ProviderIcon provider={app.clientId} size={20} className="mr-1" />
              <RowText title={app.client} />
              <RowActions>
                <span className="text-[12px] whitespace-nowrap text-tertiary">
                  {nativeProviderName(app.clientId)}
                </span>
              </RowActions>
            </Row>
          ))}
        </Group>
      ) : null}

      {confirming === 'stop' ? (
        <ConfirmDialog
          title={`Stop ${proxy.label || 'This Proxy'}?`}
          message="Apps using this proxy will not connect until it restarts."
          confirmTitle="Stop Proxy"
          testId="proxies.inspector.stop-confirm"
          onCancel={() => setConfirming(null)}
          onConfirm={() => {
            setConfirming(null);
            service.invokeAction(proxy.toggleActionId ?? '', revision);
          }}
        />
      ) : null}

      {confirming === 'rotate' ? (
        <ConfirmDialog
          title={`Rotate API Key for ${proxy.label || 'This Proxy'}?`}
          message="Apps using the current API key will be disconnected until you update them with the new key."
          confirmTitle="Rotate Key"
          testId="proxies.inspector.rotate-confirm"
          onCancel={() => setConfirming(null)}
          onConfirm={() => {
            setConfirming(null);
            service.invokeAction(proxy.rotateActionId ?? '', revision);
          }}
        />
      ) : null}
    </Dialog>
  );
}

/**
 * The proxy is gone from the snapshot.
 *
 * A detail destination for a row that no longer exists has to say so rather than
 * render an empty form of undefined fields — the relay is stopped and deleted
 * while its sheet is open often enough to be worth a branch.
 */
export function ProxyGoneSheet({ onClose }: { onClose: () => void }) {
  return (
    <Dialog
      title="Proxy Is No Longer Available"
      testId="proxies-inspector.gone"
      onCancel={onClose}
      width="form"
      actions={
        <Button kind="quiet" onClick={onClose}>
          Done
        </Button>
      }
    >
      <Group testId="proxies-inspector.gone-body">
        <Row>
          <RowText detail="Return to Proxies and choose another service." />
        </Row>
      </Group>
    </Dialog>
  );
}

function Spinner() {
  return (
    <svg viewBox="0 0 24 24" width="11" height="11" aria-hidden="true" focusable="false" className="animate-spin">
      <path
        d="M12 3a9 9 0 0 1 9 9"
        fill="none"
        stroke="currentColor"
        strokeWidth="2.4"
        strokeLinecap="round"
      />
    </svg>
  );
}

function CopyGlyph() {
  return (
    <svg viewBox="0 0 24 24" width="15" height="15" aria-hidden="true" focusable="false">
      <path
        d="M8 8h11v12H8z M5 16H4V4h12v1"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.6"
        strokeLinejoin="round"
      />
    </svg>
  );
}

function RotateGlyph() {
  return (
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
  );
}

