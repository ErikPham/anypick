import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';

import { Dialog } from '../ui/Dialog';
import { MenuSelect } from '../ui/MenuSelect';

describe('dialog focus management', () => {
  it('keeps keyboard focus inside while a portaled picker is open', async () => {
    const user = userEvent.setup();
    render(
      <Dialog title="Provider" actions={<button type="button">Done</button>}>
        <MenuSelect
          label="Provider"
          value="opencode"
          onChange={() => {}}
          options={[
            { value: 'claude', label: 'Claude' },
            { value: 'opencode', label: 'OpenCode' },
          ]}
          ariaLabel="Provider"
        />
      </Dialog>,
    );

    const trigger = screen.getByRole('combobox');
    await user.click(trigger);
    expect(document.activeElement).toBe(screen.getByRole('option', { selected: true }));

    await user.keyboard('{Tab}');
    expect(screen.getByRole('button', { name: 'Done' })).toHaveFocus();
    expect(screen.queryByRole('listbox')).not.toBeInTheDocument();
  });
});
