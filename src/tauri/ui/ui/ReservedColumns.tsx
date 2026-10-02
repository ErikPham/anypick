import type { ReactNode } from 'react';

/**
 * `ReservedColumn`. A trailing cell that holds its width whether or not it has
 * content.
 *
 * This is not cosmetic. macOS found that `Group { if … {} }.frame(width:)`
 * does **not** reserve the width: SwiftUI flattens a `Group` whose content is
 * `EmptyView`, and a frame applied to `EmptyView` is dropped, so rows with an
 * empty cell moved left and the trailing controls stopped lining up. A real
 * element fixes the size, which is what makes "the column exists whether or not
 * this row fills it" true.
 *
 * The webview has no such trap — a div is a div — so the width comes from the
 * utility and the component exists to say the column is a column.
 */
export function ReservedColumn({
  width,
  children,
  className = '',
}: {
  /** Tailwind width class, matching the macOS column it stands for. */
  width: string;
  children?: ReactNode;
  className?: string;
}) {
  return (
    <span className={`flex flex-none items-center justify-end ${width} ${className}`}>{children}</span>
  );
}