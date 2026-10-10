import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';

import { MenuItem, RowMenu } from '../ui/RowMenu';

describe('row menus', () => {
  it('moves focus through items and returns focus after Escape', async () => {
    const user = userEvent.setup();
    render(
      <RowMenu label="Actions">
        {(close) => (
          <>
            <MenuItem onSelect={close}>First</MenuItem>
            <MenuItem onSelect={close}>Second</MenuItem>
          </>
        )}
      </RowMenu>,
    );

    const trigger = screen.getByRole('button', { name: 'Actions' });
    await user.click(trigger);
    const items = screen.getAllByRole('menuitem');
    expect(document.activeElement).toBe(items[0]);

    await user.keyboard('{End}');
    expect(document.activeElement).toBe(items[1]);
    await user.keyboard('{Home}');
    expect(document.activeElement).toBe(items[0]);

    await user.keyboard('{Escape}');
    expect(screen.queryByRole('menu')).not.toBeInTheDocument();
    expect(document.activeElement).toBe(trigger);
  });

  it('announces successful and refused menu copies', async () => {
    const user = userEvent.setup();
    const clipboard = vi.fn().mockResolvedValueOnce(true).mockResolvedValueOnce(false);
    render(
      <RowMenu label="Actions" clipboard={clipboard}>
        {(_close, copy) => <MenuItem onSelect={() => copy('http://127.0.0.1:5200', 'proxy address')}>Copy address</MenuItem>}
      </RowMenu>,
    );

    await user.click(screen.getByRole('button', { name: 'Actions' }));
    await user.click(screen.getByRole('menuitem', { name: 'Copy address' }));
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('Copied proxy address.'));
    expect(screen.queryByRole('menu')).not.toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Actions' }));
    await user.click(screen.getByRole('menuitem', { name: 'Copy address' }));
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('Couldn’t copy proxy address.'));
    expect(clipboard).toHaveBeenCalledTimes(2);
  });
});
