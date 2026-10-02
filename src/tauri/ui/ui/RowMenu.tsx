import { useEffect, useId, useRef, useState, type ReactNode } from 'react';
import { createPortal } from 'react-dom';

import { usePopover } from './usePopover';

/**
 * A row's secondary commands, in a menu rather than a wall of buttons.
 *
 * The Accounts and Apps rows had four buttons each, always visible. Every row
 * shouting its own commands is the control-panel look the macOS contract bans;
 * macOS reserves a trailing column and puts the rest behind one control.
 */
export function RowMenu({
  label,
  children,
  align = 'end',
  testId,
}: {
  /** The accessible name. A menu with no name is a button that says "…". */
  label: string;
  children: (close: () => void) => ReactNode;
  align?: 'start' | 'end';
  testId?: string;
}) {
  const pop = usePopover(align);

  return (
    <div className="relative flex-none" ref={pop.root}>
      <button
        ref={pop.trigger}
        type="button"
        aria-haspopup="menu"
        aria-expanded={pop.open}
        aria-controls={pop.open ? pop.panelId : undefined}
        aria-label={label}
        title={label}
        data-testid={testId}
        className="inline-flex size-[22px] cursor-default items-center justify-center rounded-full border-0 bg-transparent p-0 text-secondary hover:bg-hover"
        onClick={() => pop.setOpen((current: boolean) => !current)}
      >
        <svg viewBox="0 0 24 24" width="15" height="15" aria-hidden="true" focusable="false">
          <circle cx="5.5" cy="12" r="1.6" fill="currentColor" />
          <circle cx="12" cy="12" r="1.6" fill="currentColor" />
          <circle cx="18.5" cy="12" r="1.6" fill="currentColor" />
        </svg>
      </button>
      {pop.open && pop.placement
        ? createPortal(
            <div
              ref={pop.panel}
              id={pop.panelId}
              role="menu"
              style={{ top: pop.placement.top, left: pop.placement.left }}
              className="fixed z-50 min-w-44 rounded-lg border border-separator-soft bg-canvas py-1 shadow-[0_8px_28px_rgb(0_0_0/0.28)]"
            >
              {children(pop.close)}
            </div>,
            document.body,
          )
        : null}
    </div>
  );
}

/** One item in a `RowMenu`. `destructive` is red text, not a filled block. */
export function MenuItem({
  children,
  onSelect,
  destructive = false,
  disabled = false,
  testId,
}: {
  children: ReactNode;
  onSelect: () => void;
  destructive?: boolean;
  disabled?: boolean;
  /**
   * A literal, in the JSX, at the call site.
   *
   * `test/parity.test.ts` finds Tauri elements by scanning source for
   * `data-testid` literals, so a menu item that carries its id through a
   * runtime-built array is invisible to it — and the parity row then reads as
   * "the UI does not emit this" when the control is right there.
   */
  testId?: string;
}) {
  return (
    <button
      type="button"
      role="menuitem"
      data-testid={testId}
      disabled={disabled}
      className={`block w-full cursor-default border-0 bg-transparent px-3 py-1 text-left text-[12px] hover:not-disabled:bg-hover disabled:cursor-default disabled:opacity-50 ${
        destructive ? 'text-red' : 'text-text'
      }`}
      onClick={onSelect}
    >
      {children}
    </button>
  );
}

/** A divider, so a destructive item is never adjacent to a live one. */
export function MenuDivider() {
  return <div role="separator" className="my-1 border-t border-separator-soft" />;
}
