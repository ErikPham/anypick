import { DESTINATIONS, type Destination } from '../lib/contract';

import { Symbol } from './Symbol';

/**
 * The sidebar: peer destinations in System Settings order.
 *
 * The order and the labels are the contract with the macOS app, and
 * `contract-drift.test.ts` pins the list. A Settings sidebar is not a nav bar
 * with arbitrary entries, so nothing is added here without a destination.
 */

const DESTINATION_LABEL: Record<Destination, string> = {
  overview: 'Overview',
  apps: 'Apps',
  accounts: 'Accounts & APIs',
  proxyHub: 'Proxy Hub',
  proxies: 'Proxies',
  activity: 'Activity',
  backups: 'Backups',
};

const DESTINATION_SYMBOL: Record<Destination, string> = {
  overview: 'square.grid.2x2',
  apps: 'app.badge',
  accounts: 'person.2',
  proxyHub: 'point.3.connected.trianglepath.dotted',
  proxies: 'server.rack',
  activity: 'list.bullet',
  backups: 'externaldrive.badge.timemachine',
};

export function Sidebar({
  destination,
  onSelect,
  state,
  runningCount,
  onOpenSettings,
}: {
  destination: Destination;
  onSelect: (destination: Destination) => void;
  state: 'starting' | 'connected' | 'stopped' | 'error';
  runningCount: number;
  onOpenSettings?: () => void;
}) {
  return (
    <aside
      className="flex min-h-0 min-w-0 flex-col overflow-auto border-r border-separator-soft bg-sidebar py-2 pr-2 pl-2"
      data-testid="sidebar-navigation"
    >
      <nav className="grid content-start gap-px" aria-label="AnyPick sections">
        {DESTINATIONS.map((item) => (
          <button
            key={item}
            type="button"
            className={`flex min-h-7 w-full items-center gap-2 rounded-control bg-transparent px-2 py-1 text-left text-[13px] font-normal hover:bg-hover ${
              destination === item ? 'bg-accent font-[590] text-white hover:bg-accent' : 'text-text'
            }`}
            onClick={() => onSelect(item)}
            aria-current={destination === item ? 'page' : undefined}
            data-testid={`sidebar-${item}`}
          >
            <span
              className={`flex-none ${destination === item ? 'text-white' : 'text-secondary'}`}
              aria-hidden="true"
            >
              <Symbol name={DESTINATION_SYMBOL[item]} />
            </span>
            <span>{DESTINATION_LABEL[item]}</span>
          </button>
        ))}
      </nav>
      <div className="mt-auto flex flex-col items-stretch gap-1.5 pt-2.5 pr-2 pb-2 pl-2">
        <div
          className="flex min-w-0 items-center gap-[7px] text-[11px] text-secondary"
          data-testid="sidebar-service-status"
        >
          <span
            className={`size-status-dot flex-none rounded-full ${
              state === 'connected'
                ? 'bg-green'
                : state === 'error'
                  ? 'bg-red'
                  : state === 'starting'
                    ? 'bg-orange'
                    : 'border-[1.5px] border-secondary/60 bg-transparent'
            }`}
            aria-hidden="true"
          />
          <span className="overflow-hidden text-ellipsis whitespace-nowrap">{statusLabel(state, runningCount)}</span>
        </div>
        {onOpenSettings ? (
          <button
            type="button"
            className="inline-flex min-h-control cursor-default items-center justify-start gap-1.5 rounded-control border border-control-border bg-control px-[11px] py-0.5 text-[12px] text-text hover:bg-hover active:bg-active"
            data-testid="sidebar-settings"
            onClick={onOpenSettings}
          >
            <Symbol name="gearshape" size={13} />
            <span>Settings</span>
          </button>
        ) : null}
      </div>
    </aside>
  );
}

function statusLabel(state: 'starting' | 'connected' | 'stopped' | 'error', runningCount: number): string {
  if (state === 'starting') return 'Connecting to the service…';
  if (state === 'error') return 'Service needs attention';
  if (state === 'stopped') return 'Service stopped';
  return `${runningCount} local service${runningCount === 1 ? '' : 's'} running`;
}