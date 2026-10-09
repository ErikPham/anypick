import { readFileSync, readdirSync } from 'node:fs';
import { join, resolve } from 'node:path';

import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';

import { Toolbar } from '../shell/Toolbar';
import { CopyButton, COPY_CONFIRMATION_MS, IconButton } from '../ui/primitives';

const uiDir = resolve(__dirname, '..');

/**
 * An icon command has three states and one owner.
 *
 * The macOS equivalent is `DesignIconButton` in `macos/SharedUI.swift`. The
 * control kind was never the parity problem — a web button paints its hover,
 * press and disabled state from CSS the way AppKit does from a bezel. The
 * problem was that the state had no owner: eight copy commands reported
 * nothing, one swapped its own glyph inline, and nothing in the suite could
 * see it because jsdom renders no stylesheet and never fires a hover.
 */
describe('an icon command owns its whole state matrix', () => {
  it('names itself while idle and working, and says Copied once it lands', () => {
    render(<CopyButton value="http://127.0.0.1:41401" label="Copy proxy address" copy={async () => true} />);
    const button = screen.getByRole('button', { name: 'Copy proxy address' });
    expect(button).toHaveAttribute('title', 'Copy proxy address');
    expect(button).not.toHaveAttribute('aria-busy');
    expect(button).toBeEnabled();
  });

  it('confirms a copy and takes it back', async () => {
    // Real time rather than fake timers: `userEvent` and a faked clock fight
    // each other, and the suite already spends seconds on slower assertions.
    const user = userEvent.setup();
    render(<CopyButton value="secret-token" label="Copy proxy API key" copy={async () => true} />);

    await user.click(screen.getByRole('button', { name: 'Copy proxy API key' }));

    // The confirmation is legible two ways at once: the glyph swaps and the
    // help tag follows, which is what the macOS control does too.
    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Copied' })).toHaveAttribute('title', 'Copied');
    });

    await waitFor(
      () => {
        expect(screen.getByRole('button', { name: 'Copy proxy API key' })).toBeInTheDocument();
      },
      { timeout: COPY_CONFIRMATION_MS + 1500 }
    );
    expect(screen.queryByRole('button', { name: 'Copied' })).not.toBeInTheDocument();
  });

  it('does not confirm a clipboard the host refused', async () => {
    // Every address in this app is also selectable text, so a refusal is not
    // worth interrupting for. Claiming it copied would be a lie told to the
    // one user who cannot select the value and read it back.
    const user = userEvent.setup();
    render(<CopyButton value="secret-token" label="Copy proxy API key" copy={async () => false} />);

    await user.click(screen.getByRole('button', { name: 'Copy proxy API key' }));
    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Copy proxy API key' })).toHaveAttribute(
        'title',
        'Copy proxy API key'
      );
    });
    expect(screen.queryByRole('button', { name: 'Copied' })).not.toBeInTheDocument();
  });

  it('will not take a second click while the first is in flight', () => {
    // `aria-busy` is the machine-readable half; `disabled` is what actually
    // stops the click, because the engine single-flights the request.
    render(
      <IconButton label="Rotate API Key" phase="working">
        <span />
      </IconButton>
    );
    const button = screen.getByRole('button', { name: /Rotate API Key/ });
    expect(button).toHaveAttribute('aria-busy', 'true');
    expect(button).toBeDisabled();
  });

  it('holds one box across every phase', () => {
    // The macOS control needs a canvas to stop a glyph swap resizing the row;
    // a fixed `size-[22px]` here makes the same guarantee structural.
    const { rerender, container } = render(
      <IconButton label="Copy" phase="idle" confirmed={<span data-testid="mark" />}>
        <span data-testid="glyph" />
      </IconButton>
    );
    const box = () => container.querySelector('button')?.getBoundingClientRect().width;
    const idle = box();
    rerender(
      <IconButton label="Copy" phase="confirmed" confirmed={<span data-testid="mark" />}>
        <span data-testid="glyph" />
      </IconButton>
    );
    expect(box()).toBe(idle);
  });

  it('greys the toolbar refresh while any command is in flight', () => {
    const base = {
      title: 'Proxies',
      searchable: false,
      query: '',
      onQuery: () => {},
      onRefresh: () => {},
      onBack: () => {},
    };
    const snapshot = {} as never;
    const { rerender } = render(<Toolbar {...base} snapshot={snapshot} busy={new Set()} />);
    expect(screen.getByTestId('toolbar-refresh')).toBeEnabled();
    rerender(<Toolbar {...base} snapshot={snapshot} busy={new Set(['req-1'])} />);
    expect(screen.getByTestId('toolbar-refresh')).toBeDisabled();
  });
});

/**
 * The rules above, held.
 *
 * Each of these regressions is invisible in a rendered test: a bezel-less
 * button still renders, a nameless tooltip still looks fine, a copy that says
 * nothing still shows a glyph. Reading the source is the only place they are
 * visible, which is the same reason `colour-tokens.test.ts` reads two files.
 */
describe('a copy command is spelled exactly once', () => {
  const paneFiles = readdirSync(join(uiDir, 'panes'))
    .filter((f) => f.endsWith('.tsx'))
    .map((f) => `panes/${f}`);
  const sources = [...paneFiles, ...readdirSync(join(uiDir, 'shell')).filter((f) => f.endsWith('.tsx')).map((f) => `shell/${f}`)];

  it('has panes to scan', () => {
    expect(paneFiles.length).toBeGreaterThan(10);
  });

  it('keeps service.copy inside CopyButton', () => {
    // Two menu commands call the clipboard directly, and the exemption is
    // written rather than inferred. A menu item has no glyph to swap and the
    // menu closes on select, so there is nowhere for a confirmation to live;
    // macOS's row menus answer the same way. Every other copy goes through
    // CopyButton, which owns the help tag, the swap and the one timer.
    const DIRECT_COPY_ALLOWED = new Map([
      ['panes/ProxiesPane.tsx', 'row-menu "Copy Address"; a menu item has no inline state'],
      ['panes/BackupsPane.tsx', 'row-menu "Copy File Path"; a menu item has no inline state'],
    ]);

    const offenders: string[] = [];
    for (const name of paneFiles) {
      const text = readFileSync(join(uiDir, name), 'utf8');
      if (/service\.copy\(/.test(text) && !DIRECT_COPY_ALLOWED.has(name)) {
        offenders.push(name);
      }
    }
    expect(
      offenders,
      `${offenders.join(', ')} calls service.copy directly. Use CopyButton: it owns the help tag, the glyph swap and the one timer, and eight call sites that each kept their own said nothing at all.`
    ).toEqual([]);

    // A bare exemption is how a gap becomes permanent without anyone
    // deciding, so an entry that no longer has a direct call must be deleted
    // rather than left to look like permission.
    const stillNeeded = [...DIRECT_COPY_ALLOWED.keys()].filter((name) =>
      /service\.copy\(/.test(readFileSync(join(uiDir, name), 'utf8'))
    );
    const dead = [...DIRECT_COPY_ALLOWED.keys()].filter((k) => !stillNeeded.includes(k));
    expect(
      dead,
      `${dead.join(', ')} no longer calls service.copy directly; delete the exemption rather than leave it reading as permission`
    ).toEqual([]);
  });

  it('draws the copy glyph once, in the shared path table', () => {
    const offenders: string[] = [];
    for (const name of paneFiles) {
      const text = readFileSync(join(uiDir, name), 'utf8');
      if (/function CopyGlyph/.test(text)) offenders.push(`${name} keeps a local CopyGlyph`);
      if (/points="20 6 9 17 4 12"/.test(text)) {
        offenders.push(`${name} draws its own checkmark`);
      }
    }
    expect(
      offenders,
      `${offenders.join(', ')} hand-draw a mark. Symbol owns the paths, so a copy and its confirmation cannot become two different glyphs.`
    ).toEqual([]);
  });

  it('resolves both marks through Symbol', () => {
    const paths = readFileSync(join(uiDir, 'shell', 'Symbol.tsx'), 'utf8');
    for (const name of ['doc.on.doc', 'checkmark']) {
      expect(paths, `Symbol has no ${name} path`).toMatch(new RegExp(`'${name.replace('.', '\\.')}':`));
    }
  });

  it('names every icon-only command', () => {
    // `label` is a required prop on IconButton, so this can only fail if a
    // caller passes an empty string. It is pinned because the help tag is the
    // only thing a pointer and VoiceOver have to go on.
    const offenders: string[] = [];
    for (const name of sources) {
      const text = readFileSync(join(uiDir, name), 'utf8');
      for (const match of text.matchAll(/<IconButton\b([^>]*?)>/gs)) {
        const attrs = match[1];
        if (/label=\{?"\s*"?\}/.test(attrs)) offenders.push(name);
      }
    }
    expect(offenders).toEqual([]);
  });
});

/**
 * The stylesheet is read here because jsdom never loads it.
 *
 * `@keyframes spin` was declared and `.spinner` never named it, so the
 * global "Connecting to the AnyPick service…" spinner stood still, and the
 * reduced-motion override set a duration on an animation that did not exist.
 * Nothing that renders in jsdom could have seen either.
 */
describe('the spinner actually turns', () => {
  const css = readFileSync(join(uiDir, 'styles.css'), 'utf8');

  it('names the keyframes it declares', () => {
    const spinner = css.match(/\.spinner\s*\{([^}]*)\}/);
    expect(spinner, 'styles.css has no .spinner rule').not.toBeNull();
    expect(
      spinner![1],
      '.spinner declares no animation, so the progress indicator is a still circle and the keyframes below it are dead code'
    ).toMatch(/animation:\s*spin\b/);
  });

  it('retards rather than removes it under Reduce Motion', () => {
    // A frozen circle says "stalled", which is the one thing this control must
    // never say, so Reduce Motion slows it and does not stop it.
    const reduced = css.match(/prefers-reduced-motion[^{]*\{[\s\S]*?\.spinner\s*\{([^}]*)\}/);
    expect(reduced, 'styles.css has no reduced-motion rule for .spinner').not.toBeNull();
    expect(reduced![1]).toMatch(/animation-duration/);
    expect(reduced![1]).not.toMatch(/animation:\s*none/);
  });
});