import { createPortal } from 'react-dom';

import { ProviderIcon } from '../shell/ProviderIcon';
import { usePopover } from './usePopover';

/**
 * `DesignProviderPicker` — the popup that names a provider.
 *
 * macOS does not use SwiftUI's `Picker` for this, and says why:
 * *"SwiftUI's Picker drops custom image labels from NSMenu items, so bridge the
 * platform control when brand identity matters."* It bridges `NSPopUpButton`
 * purely to get the brand mark into the list.
 *
 * A webview has no `NSMenu`, and a native `<select>` is worse than useless here:
 * its items are painted by the platform, so a provider row in it is a word with
 * no mark. This is the same disclosure-and-panel shape as `RowMenu`, wearing the
 * picker's bezel, with the selected item checkmarked the way `NSPopUpButton`
 * marks it.
 */
export function MenuSelect({
  label,
  value,
  onChange,
  options,
  ariaLabel,
  disabled = false,
  testId,
}: {
  /** The visible field label above the control. */
  label: string;
  value: string;
  onChange: (value: string) => void;
  options: ReadonlyArray<{
    value: string;
    label: string;
    /** The provider whose mark leads the item, as on macOS. */
    provider?: string;
  }>;
  /** Overrides the visible label as the accessible name. */
  ariaLabel?: string;
  disabled?: boolean;
  testId?: string;
}) {
  const pop = usePopover('start');
  const selected = options.find((option) => option.value === value);

  return (
    <label className="flex flex-col gap-1">
      <span className="text-[11px] text-secondary">{label}</span>
      <div className="relative flex-none" ref={pop.root}>
        <button
          ref={pop.trigger}
          type="button"
          role="combobox"
          aria-expanded={pop.open}
          aria-haspopup="listbox"
          aria-controls={pop.open ? pop.panelId : undefined}
          aria-label={ariaLabel}
          disabled={disabled}
          data-testid={testId}
          className="flex min-h-control min-w-40 max-w-55 cursor-default items-center justify-between gap-2 rounded-control border border-control-border bg-control px-2 text-left text-[12px] text-text disabled:cursor-default disabled:opacity-50"
          onClick={() => pop.setOpen((current: boolean) => !current)}
        >
          <span className="truncate">{selected?.label ?? ''}</span>
          <Chevron />
        </button>
        {pop.open && pop.placement
          ? createPortal(
              <div
                ref={pop.panel}
                id={pop.panelId}
                role="listbox"
                aria-label={ariaLabel}
                style={{ top: pop.placement.top, left: pop.placement.left }}
                className="fixed z-50 max-h-72 min-w-44 overflow-y-auto rounded-lg border border-separator-soft bg-canvas py-1 shadow-[0_8px_28px_rgb(0_0_0/0.28)]"
              >
                {options.map((option) => (
                  <Option
                    key={option.value}
                    value={option.value}
                    label={option.label}
                    provider={option.provider}
                    selected={option.value === value}
                    onSelect={() => {
                      pop.close();
                      onChange(option.value);
                    }}
                  />
                ))}
              </div>,
              document.body,
            )
          : null}
      </div>
    </label>
  );
}

/** One item. The mark leads the label, as `NSPopUpButton` lays it out. */
function Option({
  value,
  label,
  provider,
  selected,
  onSelect,
}: {
  value: string;
  label: string;
  provider?: string;
  selected: boolean;
  onSelect: () => void;
}) {
  return (
    <button
      type="button"
      role="option"
      aria-selected={selected}
      data-value={value}
      onClick={onSelect}
      className="flex w-full cursor-default items-center gap-2 border-0 bg-transparent px-2 py-1 text-left text-[12px] text-text hover:bg-hover"
    >
      {/* The checkmark column is always reserved, so items do not jump sideways
          when the selection changes. */}
      <span className="w-3 flex-none text-accent">{selected ? '✓' : ''}</span>
      {provider ? <ProviderIcon provider={provider} size={16} /> : null}
      <span className="truncate">{label}</span>
    </button>
  );
}

function Chevron() {
  return (
    <svg viewBox="0 0 24 24" width="11" height="11" aria-hidden="true" focusable="false" className="flex-none text-secondary">
      <path
        d="M6 9l6 6 6-6"
        fill="none"
        stroke="currentColor"
        strokeWidth="2"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}
