/**
 * `.pickerStyle(.segmented)`. macOS uses it wherever a control chooses between
 * a small, fixed set of named options, and a webview has no equivalent — a
 * native `<select>` reads as a dropdown, which hides the other options behind
 * a click and is a different control from the one the macOS contract names.
 *
 * The selected half is filled rather than tinted: two `bg-*` utilities on one
 * element resolve by stylesheet order, not by class-attribute order, so the
 * unselected class must not be able to win.
 */
export function SegmentedControl({
  value,
  onChange,
  options,
  disabled = false,
  testId,
  className = '',
}: {
  value: string;
  onChange: (value: string) => void;
  options: ReadonlyArray<{ value: string; label: string }>;
  disabled?: boolean;
  testId?: string;
  className?: string;
}) {
  return (
    // `flex-none` and `whitespace-nowrap`: in a trailing column the control
    // used to shrink and wrap its own labels onto two lines — "Live Logs" broke
    // after "Live" — which is a broken control, not a compact one.
    <div
      className={`inline-flex flex-none items-center p-[2px] rounded-[7px] bg-[rgba(0,0,0,0.06)] dark:bg-[rgba(255,255,255,0.08)] border border-[rgba(0,0,0,0.04)] dark:border-[rgba(255,255,255,0.05)] disabled:opacity-45 ${className}`}
      data-testid={testId}
    >
      {options.map((option) => (
        <button
          key={option.value}
          type="button"
          disabled={disabled}
          aria-pressed={value === option.value}
          className={`min-h-[22px] flex-1 cursor-default border-0 px-3 py-0.5 text-[12px] whitespace-nowrap rounded-[5px] transition-all duration-100 ease-out disabled:cursor-default ${
            value === option.value
              ? 'bg-white dark:bg-[#636366] text-black dark:text-white font-medium shadow-[0_1px_2.5px_rgba(0,0,0,0.18)]'
              : 'bg-transparent text-secondary hover:text-text font-normal'
          }`}
          onClick={() => onChange(option.value)}
        >
          {option.label}
        </button>
      ))}
    </div>
  );
}