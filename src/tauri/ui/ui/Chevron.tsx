/**
 * The disclosure chevron a `NavigationLink` draws at a row's trailing edge.
 *
 * One component because three panes needed it and a glyph that must be
 * identical in three places will not stay identical when it is copy-pasted. The
 * detail sheets draw a smaller one (`DisclosureChevron`) beside a link's label,
 * where it annotates rather than discloses.
 */
export function Chevron() {
  return (
    <svg viewBox="0 0 24 24" width="12" height="12" aria-hidden="true" focusable="false">
      <path
        d="m9 5 7 7-7 7"
        fill="none"
        stroke="currentColor"
        strokeWidth="2"
        strokeLinecap="round"
        strokeLinejoin="round"
        className="text-tertiary"
      />
    </svg>
  );
}

/** The smaller mark beside "View in Activity" and other inline destinations. */
export function DisclosureChevron() {
  return (
    <svg viewBox="0 0 24 24" width="9" height="9" aria-hidden="true" focusable="false">
      <path
        d="m9 5 7 7-7 7"
        fill="none"
        stroke="currentColor"
        strokeWidth="2.4"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}
