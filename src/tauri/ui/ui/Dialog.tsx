import { useEffect, useId, useRef, type ReactNode } from 'react';

/**
 * A modal dialog, the webview's stand-in for SwiftUI's
 * `confirmationDialog` and `.sheet`.
 *
 * The macOS contract bans a custom modal card: a scrim, a titled sheet, and
 * Cancel beside the destructive default on the trailing edge. Those three
 * parts are what make it a System Settings dialog rather than a web page, so
 * they live in one component instead of being re-spelled per pane.
 *
 * Focus is trapped and restored because a webview has no native sheet to do it.
 * Without it, Tab walks out of the dialog into the pane behind it and a
 * keyboard user can edit the very settings the dialog is asking about.
 */
export function Dialog({
  title,
  message,
  children,
  actions,
  onCancel,
  testId,
  widthTestId,
  width = 'default',
}: {
  title: string;
  message?: ReactNode;
  /** Form fields. Sits under the message and above the actions. */
  children?: ReactNode;
  /**
   * The trailing buttons. Omit it for a form-only sheet that closes another
   * way; macOS puts Cancel on the trailing edge for a confirmation and leaves
   * it off a sheet with nothing to cancel.
   */
  actions?: ReactNode;
  onCancel?: () => void;
  testId?: string;
  /** An id for the dialog body, when the scrim carries a different one. */
  widthTestId?: string;
  width?: 'default' | 'form' | 'inspector';
}) {
  const body = useRef<HTMLDivElement>(null);
  const dialogId = useId();
  const cancelAction = useRef(onCancel);
  cancelAction.current = onCancel;

  useEffect(() => {
    const previouslyFocused = document.activeElement as HTMLElement | null;
    const root = body.current;
    if (!root) return;

    const focusable = (): HTMLElement[] => {
      const selector = 'button, input, select, textarea, [tabindex]';
      const inside = Array.from(root.querySelectorAll<HTMLElement>(selector));
      const ordered = inside.flatMap((node) => {
        const panelId = node.getAttribute('aria-controls');
        const panel = panelId ? document.getElementById(panelId) : null;
        const panelItems = panel?.getAttribute('data-dialog-owner') === dialogId
          ? Array.from(panel.querySelectorAll<HTMLElement>(selector))
          : [];
        return [node, ...panelItems];
      });
      return [...new Set(ordered)].filter(
        (node) => !node.hasAttribute('disabled') && node.tabIndex !== -1,
      );
    };

    // Focus the first control so a keyboard user starts inside the sheet. The
    // dialog's own heading is not focusable, so this never lands on the title.
    const [first] = focusable();
    first?.focus();

    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        const target = event.target as Element | null;
        if (target?.closest('[data-dialog-owner]')?.getAttribute('data-dialog-owner') === dialogId) return;
        event.stopPropagation();
        cancelAction.current?.();
        return;
      }
      if (event.key !== 'Tab') return;
      const nodes = focusable();
      if (nodes.length === 0) return;
      const index = nodes.indexOf(document.activeElement as HTMLElement);
      const next = index < 0
        ? event.shiftKey ? nodes.length - 1 : 0
        : (index + (event.shiftKey ? -1 : 1) + nodes.length) % nodes.length;
      event.preventDefault();
      nodes[next].focus();
    };

    document.addEventListener('keydown', onKeyDown);
    return () => {
      document.removeEventListener('keydown', onKeyDown);
      // Hand focus back where it was, or the click that opened the sheet
      // leaves focus on a detached node and the next Tab starts at the top.
      previouslyFocused?.focus?.();
    };
  }, [dialogId]);

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-6 backdrop-blur-[1px]"
      // A scrim click is a cancel, which is what macOS does for a sheet. The
      // keyboard path is Escape because a mouse is not always present.
      onClick={(event) => {
        if (event.target === event.currentTarget) onCancel?.();
      }}
    >
      <div
        ref={body}
        id={dialogId}
        role="dialog"
        aria-modal="true"
        aria-label={title}
        // The body's id, not the scrim's. A scrim is presentation, and putting
        // the same test id on both made `getByTestId` ambiguous for the dialog.
        data-testid={testId ?? widthTestId}
        /*
         * The title and the buttons are pinned; only the body scrolls.
         *
         * A SwiftUI sheet pins both — the footer is outside the scroll view — and
         * it matters when a sheet is taller than its window: pinning the
         * actions keeps the way out visible while the form itself scrolls.
         */
        className={`flex max-h-full flex-col overflow-hidden rounded-xl border border-separator-soft bg-canvas px-5 py-4 shadow-[0_16px_48px_rgba(0,0,0,0.36)] ${
          width === 'inspector' ? 'w-full max-w-[520px]' : width === 'form' ? 'w-full max-w-[460px]' : 'w-full max-w-[380px]'
        }`}
      >
        <h2 className="shrink-0 text-[14px] font-semibold text-text">{title}</h2>
        {message ? (
          <p className="m-0 shrink-0 text-[12px] leading-[1.4] text-secondary">{message}</p>
        ) : null}
        <div className="min-h-0 flex-1 overflow-auto">{children}</div>
        {actions ? (
          <div className="flex shrink-0 justify-end gap-2 pt-2.5">{actions}</div>
        ) : null}
      </div>
    </div>
  );
}
