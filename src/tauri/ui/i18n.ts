/**
 * User-facing strings that are not product copy.
 *
 * Everything a pane says about AnyPick's own behaviour lives in the pane, next
 * to the rule that produces it, because that copy is the product's copy and it
 * is shared with the macOS app. What lives here is the handful of strings that
 * describe the connection to the engine rather than AnyPick itself, so there is
 * one place to look when the service starts slowly on a cold cache.
 */
export const ui = {
  connecting: 'Connecting to the AnyPick service…',
  loading: 'Loading AnyPick…',
} as const;
