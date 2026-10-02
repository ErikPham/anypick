import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { AccountsPane } from '../panes';
import { createService } from '../lib/service';
import { FakeBridge, snapshotFixture } from './harness';

describe('dbg', () => {
  it('icon', () => {
    const bridge = new FakeBridge();
    const { port } = createService({ bridge });
    const { container } = render(<AccountsPane service={port} snapshot={snapshotFixture()} />);
    const imgs = container.querySelectorAll('img');
    console.log('IMGS:', imgs.length, [...imgs].map((i) => i.getAttribute('src')));
    console.log('ROW HTML:', container.querySelector('[data-testid^="accounts.row"]')?.innerHTML.slice(0, 300));
    expect(true).toBe(true);
  });
});
