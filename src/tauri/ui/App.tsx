import { useEffect, useMemo, useState } from 'react';
import { createBridge } from './lib/bridge';
import { DESTINATIONS, isDestination, type Destination, type Snapshot } from './lib/contract';

/**
 * The AnyPick desktop app for Linux and Windows.
 *
 * Structure mirrors the macOS app: a sidebar of destinations beside a detail
 * pane, with a status bar showing the live connection to the Go service. Data
 * comes only from that service — there is no fixture and no browser fallback.
 */

const bridge = createBridge();

type ServiceState = 'starting' | 'connected' | 'stopped' | 'error';

type EngineFrame = {
  type?: string;
  snapshot?: Snapshot;
  message?: string;
  status?: string;
};

const DESTINATION_LABEL: Record<Destination, string> = {
  overview: 'Overview',
  apps: 'Apps',
  accounts: 'Accounts & APIs',
  proxyHub: 'Proxy Hub',
  proxies: 'Proxies',
  activity: 'Activity',
  backups: 'Backups',
};

const DESTINATION_ICON: Record<Destination, string> = {
  overview: '◉',
  apps: '⬚',
  accounts: '⌘',
  proxyHub: '⇄',
  proxies: '⇅',
  activity: '◎',
  backups: '⛁',
};

// btoa throws on non-ASCII (Unicode paths/labels); encode UTF-8 bytes.
// Mirrors decodeB64Utf8 below, which decodes the same way.
function encodeB64Utf8(text: string): string {
  const bytes = new TextEncoder().encode(text);
  let bin = '';
  for (const byte of bytes) bin += String.fromCharCode(byte);
  return btoa(bin);
}

export function App() {
  const [destination, setDestination] = useState<Destination>('overview');
  const [snapshot, setSnapshot] = useState<Snapshot | null>(null);
  const [state, setState] = useState<ServiceState>('starting');
  const [notice, setNotice] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    const unlisten: Array<() => void> = [];

    // Engine frames are `kind\tbase64(json)` (see go/internal/desktop/protocol.go
    // frame() and rust protocol.rs canonical_payload). The Rust layer forwards
    // the raw line on `service-line`; decode here instead of JSON.parse on the
    // whole payload, which can never succeed on a framed line.
    // Base64 may carry non-ASCII JSON, so decode bytes as UTF-8 rather than
    // treating atob output as a JS string directly.
    const decodeB64Utf8 = (encoded: string): string => {
      const bin = atob(encoded);
      const bytes = new Uint8Array(bin.length);
      for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
      return new TextDecoder().decode(bytes);
    };
    // btoa throws on non-ASCII (Unicode paths/labels); the module-scope
    // encodeB64Utf8 handles the send direction.
    const decodeFrame = (line: string): EngineFrame | null => {
      const tab = line.indexOf('\t');
      if (tab < 0) return null;
      const kind = line.slice(0, tab);
      const encoded = line.slice(tab + 1);
      if (kind !== 'snapshot' && kind !== 'snapshot-event' && kind !== 'result' &&
          kind !== 'logs' && kind !== 'backup-preview' && kind !== 'usage-event' &&
          kind !== 'protocol-error') {
        return null;
      }
      try {
        const json = decodeB64Utf8(encoded);
        const payload = JSON.parse(json) as Record<string, unknown>;
        if (kind === 'snapshot' || kind === 'snapshot-event') {
          // snapshot frames carry the Snapshot inline; snapshot-event wraps
          // it under `snapshot`.
          const snap = (payload.snapshot ?? payload) as Snapshot;
          if (typeof snap.revision === 'number') {
            return { type: kind, snapshot: snap };
          }
          return null;
        }
        if (kind === 'result' || kind === 'protocol-error') {
          const message = typeof payload.message === 'string' ? payload.message : undefined;
          const status = typeof payload.status === 'string' ? payload.status : undefined;
          return { type: kind, message, status };
        }
        return { type: kind };
      } catch {
        return null;
      }
    };

    void bridge.listen('service-line', (event) => {
      if (cancelled) return;
      const line = String((event.payload as unknown) ?? '');
      const frame = decodeFrame(line);
      if (!frame) return; // Diagnostic line or undecodable: ignore, don't crash.
      if (frame.snapshot) {
        setSnapshot(frame.snapshot);
        setState('connected');
      }
      if (frame.message) setNotice(frame.message);
    }).then((fn) => unlisten.push(fn));

    void bridge.listen('service-state', (event) => {
      if (cancelled) return;
      const value = String(event.payload).toLowerCase();
      if (value.includes('error')) setState('error');
      else if (value.includes('stopped') || value.includes('exit')) setState('stopped');
      else if (value.includes('ready') || value.includes('running')) setState('connected');
    }).then((fn) => unlisten.push(fn));

    return () => {
      cancelled = true;
      for (const fn of unlisten) fn();
    };
  }, []);

  const statusLine = useMemo(() => {
    if (state === 'connected' && snapshot) {
      return `Connected · revision ${snapshot.revision} · ${snapshot.proxyCount} proxy running`;
    }
    if (state === 'error') return 'Go service error — see Activity';
    if (state === 'stopped') return 'Go service stopped';
    return 'Connecting to Go service…';
  }, [state, snapshot]);

  async function send(command: string) {
    await bridge.invoke('send_command', { command });
  }

  async function sendMutate(operation: string, extra: Record<string, unknown> = {}) {
    const payload = {
      version: 1,
      requestId: `${operation}-${Date.now()}`,
      operation,
      ...extra,
    };
    await send(`mutate\t${encodeB64Utf8(JSON.stringify(payload))}`);
  }

  return (
    <div className="flex h-screen bg-neutral-50 text-neutral-900 dark:bg-neutral-900 dark:text-neutral-100">
      <nav className="w-56 shrink-0 border-r border-neutral-200 p-3 dark:border-neutral-800">
        <div className="mb-3 px-2 text-sm font-semibold">AnyPick</div>
        <ul className="space-y-1">
          {DESTINATIONS.map((item) => (
            <li key={item}>
              <button
                type="button"
                onClick={() => setDestination(item)}
                className={`flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm ${
                  destination === item
                    ? 'bg-neutral-200 dark:bg-neutral-800'
                    : 'hover:bg-neutral-100 dark:hover:bg-neutral-800/60'
                }`}
              >
                <span aria-hidden>{DESTINATION_ICON[item]}</span>
                {DESTINATION_LABEL[item]}
              </button>
            </li>
          ))}
        </ul>
      </nav>

      <main className="flex min-w-0 flex-1 flex-col">
        <header className="border-b border-neutral-200 px-6 py-3 dark:border-neutral-800">
          <h1 className="text-lg font-semibold">{DESTINATION_LABEL[destination]}</h1>
        </header>

        <section className="min-h-0 flex-1 overflow-auto px-6 py-4">
          <DestinationView
            destination={destination}
            snapshot={snapshot}
            onSend={(command) => void send(command)}
            onMutate={(operation, extra) => void sendMutate(operation, extra)}
          />
          {notice ? (
            <p className="mt-4 rounded-md bg-amber-100 px-3 py-2 text-sm text-amber-900 dark:bg-amber-900/40 dark:text-amber-100">
              {notice}
            </p>
          ) : null}
        </section>

        <footer className="border-t border-neutral-200 px-6 py-2 text-xs text-neutral-500 dark:border-neutral-800">
          {statusLine}
        </footer>
      </main>
    </div>
  );
}

function DestinationView({
  destination,
  snapshot,
  onSend,
  onMutate,
}: {
  destination: Destination;
  snapshot: Snapshot | null;
  onSend: (command: string) => void;
  onMutate: (operation: string, extra?: Record<string, unknown>) => void;
}) {
  if (!snapshot) {
    return <p className="text-sm text-neutral-500">Waiting for the first snapshot…</p>;
  }

  switch (destination) {
    case 'overview':
      return (
        <dl className="grid grid-cols-2 gap-4 text-sm">
          <Stat label="Apps routed" value={snapshot.actions.length} />
          <Stat label="Saved accounts" value={snapshot.accounts.length} />
          <Stat label="Proxies" value={snapshot.proxies.length} />
          <Stat label="Hub sources" value={snapshot.hubSources.length} />
        </dl>
      );

    case 'apps':
      return <RowList rows={snapshot.actions.map((row) => ({
        id: row.id,
        title: `${row.client} → ${row.label}`,
        detail: row.detail ?? '',
        trailing: row.selected ? 'Selected' : '',
      }))} />;

    case 'accounts':
      return <RowList rows={snapshot.accounts.map((row) => ({
        id: row.id,
        title: row.label,
        detail: row.detail,
        trailing: row.active ? 'Active' : '',
      }))} />;

    case 'proxies':
      return (
        <>
          <RowList rows={snapshot.proxies.map((row) => ({
            id: row.id,
            title: row.label,
            detail: row.address ? `${row.detail} · ${row.address}` : row.detail,
            trailing: row.running ? 'Running' : row.enabled ? 'Enabled' : 'Disabled',
          }))} />
          <button
            type="button"
            className="mt-4 rounded-md border px-3 py-1.5 text-sm"
            onClick={() => onSend('refresh')}
          >
            Refresh proxy state
          </button>
        </>
      );

    case 'proxyHub':
      return <RowList rows={snapshot.hubSources.map((row, index) => ({
        id: String(row.id ?? index),
        title: String(row.label ?? row.id ?? 'Hub source'),
        detail: String(row.detail ?? ''),
        trailing: '',
      }))} />;

    case 'activity':
      return <RowList rows={snapshot.activity.map((row, index) => ({
        id: String(row.id ?? index),
        title: String(row.title ?? row.kind ?? 'Event'),
        detail: String(row.detail ?? ''),
        trailing: String(row.at ?? ''),
      }))} />;

    case 'backups':
      return (
        <button
          type="button"
          className="rounded-md border px-3 py-1.5 text-sm"
          // Exact shape pinned by TestStrictBackupAllContract: path:"" falls
          // back to a timestamped temp file (no file picker in this shell UI;
          // `overwrite` is not an allowed key).
          onClick={() => onMutate('account-backup-all', { path: '' })}
        >
          Create backup
        </button>
      );
  }
}

function Stat({ label, value }: { label: string; value: number }) {
  return (
    <div className="rounded-lg border border-neutral-200 p-4 dark:border-neutral-800">
      <dt className="text-xs uppercase text-neutral-500">{label}</dt>
      <dd className="mt-1 text-2xl font-semibold">{value}</dd>
    </div>
  );
}

function RowList({
  rows,
}: {
  rows: Array<{ id: string; title: string; detail: string; trailing: string }>;
}) {
  if (rows.length === 0) {
    return <p className="text-sm text-neutral-500">Nothing here yet.</p>;
  }
  return (
    <ul className="divide-y divide-neutral-200 dark:divide-neutral-800">
      {rows.map((row) => (
        <li key={row.id} className="flex items-center justify-between gap-4 py-2">
          <div className="min-w-0">
            <div className="truncate text-sm font-medium">{row.title}</div>
            {row.detail ? (
              <div className="truncate text-xs text-neutral-500">{row.detail}</div>
            ) : null}
          </div>
          {row.trailing ? (
            <span className="shrink-0 text-xs text-neutral-500">{row.trailing}</span>
          ) : null}
        </li>
      ))}
    </ul>
  );
}

export { isDestination };
