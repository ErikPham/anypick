import { useEffect, useId, useLayoutEffect, useRef, useState, type KeyboardEvent as ReactKeyboardEvent } from 'react';

const GAP = 6;

/**
 * The mechanics every popover in this app needs, once.
 *
 * A webview has no system `Menu` or `NSPopUpButton`, so both the row menu and
 * the picker below are a disclosure button plus a panel. Keeping the placement
 * here rather than in each one is not tidiness for its own sake: the panel is
 * portalled to `document.body` on purpose. In place it was clipped by the
 * group's `overflow-hidden` and by the pane's scroll container, so on the first
 * row of a pane its items rendered off-screen and could not be clicked — which
 * is why jsdom tests drove them happily while the real app could not.
 *
 * So the panel is positioned against the trigger's *viewport* box, flips above
 * when there is no room below, and closes on Escape, an outside pointer-down, a
 * resize or a scroll — the last because a scrolled panel would otherwise stay
 * pinned to a trigger that has moved out from under it.
 */
export function usePopover(align: 'start' | 'end' = 'end', initialFocus: 'first' | 'selected' = 'first') {
  const [open, setOpen] = useState(false);
  // Where the panel goes, measured from the trigger. `null` until opened, so a
  // closed popover has no positioned node to pay for.
  const [placement, setPlacement] = useState<{ top: number; left: number } | null>(null);
  const root = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const panel = useRef<HTMLDivElement>(null);
  const panelId = useId();

  useLayoutEffect(() => {
    if (!open) {
      setPlacement(null);
      return;
    }
    const place = () => {
      const box = trigger.current?.getBoundingClientRect();
      if (!box) return;
      const height = panel.current?.offsetHeight ?? 0;
      const width = panel.current?.offsetWidth ?? 0;
      const below = box.bottom + GAP + height <= window.innerHeight;
      setPlacement({
        top: below ? box.bottom + GAP : Math.max(GAP, box.top - GAP - height),
        left: Math.max(
          GAP,
          Math.min(
            align === 'end' ? box.right - width : box.left,
            window.innerWidth - width - GAP,
          ),
        ),
      });
    };
    place();
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        setOpen(false);
        trigger.current?.focus();
      }
    };
    const onPointerDown = (event: PointerEvent) => {
      const target = event.target as Node;
      if (!root.current?.contains(target) && !panel.current?.contains(target)) setOpen(false);
    };
    const onFocusIn = (event: FocusEvent) => {
      const target = event.target as Node;
      if (!root.current?.contains(target) && !panel.current?.contains(target)) setOpen(false);
    };
    const dismiss = () => setOpen(false);
    document.addEventListener('keydown', onKeyDown);
    document.addEventListener('pointerdown', onPointerDown);
    document.addEventListener('focusin', onFocusIn);
    window.addEventListener('resize', dismiss);
    window.addEventListener('scroll', dismiss, true);
    return () => {
      document.removeEventListener('keydown', onKeyDown);
      document.removeEventListener('pointerdown', onPointerDown);
      document.removeEventListener('focusin', onFocusIn);
      window.removeEventListener('resize', dismiss);
      window.removeEventListener('scroll', dismiss, true);
    };
    // `place` is intentionally not a dependency: measuring after the panel has
    // mounted gives its real size, and measuring on every render would move it
    // under the pointer while it is being read.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, align]);

  useEffect(() => {
    if (!open || !placement) return;
    const items = panel.current?.querySelectorAll<HTMLElement>(
      '[role="menuitem"]:not(:disabled), [role="option"]:not(:disabled)',
    );
    if (!items?.length) return;
    const selected = initialFocus === 'selected'
      ? panel.current?.querySelector<HTMLElement>('[role="option"][aria-selected="true"]')
      : null;
    (selected ?? items[0]).focus();
  }, [open, placement, initialFocus]);

  const onPanelKeyDown = (event: ReactKeyboardEvent<HTMLElement>) => {
    if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return;
    const items = Array.from(
      panel.current?.querySelectorAll<HTMLElement>(
        '[role="menuitem"]:not(:disabled), [role="option"]:not(:disabled)',
      ) ?? [],
    );
    if (items.length === 0) return;
    const index = items.indexOf(document.activeElement as HTMLElement);
    let next = index;
    if (event.key === 'Home') next = 0;
    else if (event.key === 'End') next = items.length - 1;
    else if (event.key === 'ArrowDown') next = (index + 1) % items.length;
    else next = (index <= 0 ? items.length : index) - 1;
    event.preventDefault();
    items[next].focus();
  };

  return {
    open,
    setOpen,
    placement,
    panelId,
    root,
    trigger,
    panel,
    dialogOwner: root.current?.closest<HTMLElement>('[role="dialog"]')?.id,
    onPanelKeyDown,
    /** Closes and returns focus, the way a platform menu does. */
    close: () => {
      setOpen(false);
      trigger.current?.focus();
    },
  };
}
