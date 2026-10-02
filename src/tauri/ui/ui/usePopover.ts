import { useEffect, useId, useRef, useState } from 'react';

const GAP = 6;
/** Only used for the first measurement, before the panel has been laid out. */
const ESTIMATED_HEIGHT = 132;
const ESTIMATED_WIDTH = 190;

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
export function usePopover(align: 'start' | 'end' = 'end') {
  const [open, setOpen] = useState(false);
  // Where the panel goes, measured from the trigger. `null` until opened, so a
  // closed popover has no positioned node to pay for.
  const [placement, setPlacement] = useState<{ top: number; left: number } | null>(null);
  const root = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const panel = useRef<HTMLDivElement>(null);
  const panelId = useId();

  useEffect(() => {
    if (!open) {
      setPlacement(null);
      return;
    }
    const place = () => {
      const box = trigger.current?.getBoundingClientRect();
      if (!box) return;
      const height = panel.current?.offsetHeight || ESTIMATED_HEIGHT;
      const width = panel.current?.offsetWidth || ESTIMATED_WIDTH;
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
    const dismiss = () => setOpen(false);
    document.addEventListener('keydown', onKeyDown);
    document.addEventListener('pointerdown', onPointerDown);
    window.addEventListener('resize', dismiss);
    window.addEventListener('scroll', dismiss, true);
    return () => {
      document.removeEventListener('keydown', onKeyDown);
      document.removeEventListener('pointerdown', onPointerDown);
      window.removeEventListener('resize', dismiss);
      window.removeEventListener('scroll', dismiss, true);
    };
    // `place` is intentionally not a dependency: re-measuring on every render
    // would move the panel under the pointer while it is being read.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, align]);

  return {
    open,
    setOpen,
    placement,
    panelId,
    root,
    trigger,
    panel,
    /** Closes and returns focus, the way a platform menu does. */
    close: () => {
      setOpen(false);
      trigger.current?.focus();
    },
  };
}
