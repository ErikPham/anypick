import { useEffect, useState } from 'react';

import { brandFileFor, fallbackSymbolFor, rendersOriginal, tintFor } from '../lib/brands';
import { Symbol } from './Symbol';

/**
 * `DesignProviderIcon`. The row's brand mark, or a provider-specific glyph.
 *
 * macOS draws this in one view and nowhere else: *"Chrome uses SF Symbols; only
 * this view loads brand files."* This is that view for the webview.
 *
 * The point of loading a brand file at all is that an unbundled provider still
 * gets a mark that says *which* provider it is. A webview cannot load a macOS
 * resource bundle, so the files are vendored into `assets/icons/` and the
 * fallback glyph comes from the same table macOS uses — so a provider with no
 * mark here wears the same semantic symbol it would wear on macOS.
 */
export function ProviderIcon({
  provider,
  size = 22,
  fallbackSymbol,
  className = '',
}: {
  provider: string;
  size?: number;
  /** Overrides the table's glyph. macOS takes this as a parameter. */
  fallbackSymbol?: string;
  className?: string;
}) {
  const file = brandFileFor(provider);
  const glyph = fallbackSymbol ?? fallbackSymbolFor(provider);
  const tint = tintFor(provider);
  const [failed, setFailed] = useState(false);

  // A new provider resets the failure: the previous one not having a mark says
  // nothing about this one.
  useEffect(() => setFailed(false), [provider]);

  // `publicDir` is `assets/` (`vite.config.ts`), so `assets/icons/x.svg` is
  // served at `icons/x.svg` — not at `assets/icons/x.svg`. `base` is `./`, so
  // the URL is relative to the document, which is what lets the same bundle work
  // over a loopback server and from the packaged app.
  const url = file ? `${import.meta.env.BASE_URL}icons/${file}` : '';

  /*
   * A full-colour mark renders as it is. Every other bundled mark is a single
   * white or black fill: shown raw it is invisible on light and mud on dark, so
   * macOS sets `image.isTemplate` and tints it with `DesignBrandAsset.tint`.
   *
   * A `filter` chain cannot do that — it has no way to name "this provider's
   * brand purple". A CSS mask can: the mark is used as the mask and the
   * background is the colour, which is the web equivalent of a template image
   * with a tint. `tintFor` returns null for the marks that are black-on-light /
   * white-on-dark by design, and those take the label colour instead.
   */
  if (file && !failed) {
    if (rendersOriginal(file)) {
      return (
        <img
          src={url}
          alt=""
          width={size}
          height={size}
          aria-hidden="true"
          className={`flex-none object-contain ${className}`}
          onError={() => setFailed(true)}
        />
      );
    }
    return (
      <span
        aria-hidden="true"
        className={`flex-none ${className}`}
        style={{
          width: size,
          height: size,
          backgroundColor: tint ?? 'var(--color-text)',
          maskImage: `url(${url})`,
          WebkitMaskImage: `url(${url})`,
          maskSize: 'contain',
          WebkitMaskSize: 'contain',
          maskRepeat: 'no-repeat',
          WebkitMaskRepeat: 'no-repeat',
          maskPosition: 'center',
          WebkitMaskPosition: 'center',
          opacity: 0.85,
        }}
        // A mask that fails to load paints nothing at all, so the failure has to
        // fall back to the glyph rather than leave a hole in the row.
        ref={(node) => {
          if (!node) return;
          const image = new Image();
          image.onerror = () => setFailed(true);
          image.src = url;
        }}
      />
    );
  }

  return (
    <span
      className={`inline-flex flex-none items-center justify-center ${className}`}
      style={tint ? { color: tint } : undefined}
    >
      <Symbol name={glyph} size={size} />
    </span>
  );
}
