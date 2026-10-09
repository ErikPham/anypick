import type { Snapshot } from '../lib/contract';
import { Symbol } from './Symbol';

/**
 * The pane toolbar: the pane's title, a search field where the list is long
 * enough to need one, and a refresh.
 *
 * The macOS README keeps collection actions out of the titlebar and says why:
 * on macOS 26 a toolbar item lifts SwiftUI buttons into the chrome, so the
 * search field is an AppKit bridge. A webview has no such behaviour, which is
 * the one place this port is simpler than the source rather than different.
 */
export function Toolbar({
  title,
  searchable,
  query,
  onQuery,
  onRefresh,
  snapshot,
  busy,
  onBack,
}: {
  title: string;
  searchable: boolean;
  query: string;
  onQuery: (value: string) => void;
  onRefresh: () => void;
  snapshot: Snapshot | null;
  /**
   * Any command in flight. The refresh greys rather than spins: the engine
   * single-flights, so a second refresh lands on the same slot, and macOS
   * makes the same call on `OverviewView`'s Refresh.
   */
  busy: ReadonlySet<string>;
  onBack?: () => void;
}) {
  return (
    <header className="flex min-h-11 items-center justify-between gap-4 bg-transparent px-5 pt-2.5 pb-1.5">
      <div className="flex min-w-0 flex-1 items-center gap-2">
        {onBack ? (
          <button
            type="button"
            className="inline-flex size-6 flex-none cursor-default items-center justify-center rounded-control border border-control-border bg-control text-secondary hover:not-disabled:bg-hover hover:not-disabled:text-text disabled:opacity-50"
            onClick={onBack}
            aria-label="Back"
            data-testid="toolbar-back"
          >
            <svg viewBox="0 0 24 24" width="14" height="14" aria-hidden="true" focusable="false">
              <path
                d="M15 18l-6-6 6-6"
                fill="none"
                stroke="currentColor"
                strokeWidth="2"
                strokeLinecap="round"
                strokeLinejoin="round"
              />
            </svg>
          </button>
        ) : null}
        <h1 className="min-w-0 flex-1 overflow-hidden text-[15px] font-semibold tracking-[-0.01em] text-ellipsis whitespace-nowrap">
          {title}
        </h1>
      </div>
      <div className="flex flex-none items-center gap-2">
        {searchable ? (
          <label className="relative flex h-[22px] w-45 items-center gap-1.5 rounded-control border border-transparent bg-text/6 px-[7px] text-tertiary focus-within:border-accent focus-within:bg-group">
            {/* The label is spoken, not drawn. Without it the placeholder is
                the only name and a screen reader reads a bare "edit". */}
            <span className="sr-only">Search {title}</span>
            <span className="flex-none text-tertiary" aria-hidden="true">
              <Symbol name="magnifyingglass" size={11} />
            </span>
            <input
              className="w-full min-w-0 border-0 bg-transparent p-0 text-[12px] text-text outline-none placeholder:text-tertiary"
              type="search"
              value={query}
              onChange={(event) => onQuery(event.target.value)}
              placeholder={`Search ${title}`}
              data-testid="toolbar-search"
            />
            {query ? (
              <button
                type="button"
                className="flex-none cursor-default text-tertiary hover:text-text"
                onClick={() => onQuery('')}
                aria-label="Clear search"
              >
                <Symbol name="xmark.circle" size={11} />
              </button>
            ) : null}
          </label>
        ) : null}
        <button
          type="button"
          className="inline-flex size-6 cursor-default items-center justify-center rounded-control border border-control-border bg-control text-secondary hover:not-disabled:bg-hover hover:not-disabled:text-text disabled:opacity-50"
          onClick={onRefresh}
          aria-label="Refresh"
          title="Refresh"
          data-testid="toolbar-refresh"
          aria-busy={busy.size > 0 || undefined}
          disabled={!snapshot || busy.size > 0}
        >
          <svg
            viewBox="0 0 24 24"
            width="16"
            height="16"
            fill="none"
            stroke="currentColor"
            strokeWidth="1.6"
            strokeLinecap="round"
            aria-hidden="true"
            focusable="false"
          >
            <path d="M20 11a8 8 0 1 0 1 4 M20 5v6h-6" />
          </svg>
        </button>
      </div>
    </header>
  );
}