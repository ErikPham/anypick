import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';

import { MenuSelect } from '../ui/MenuSelect';

const OPTIONS = [
  { value: '', label: 'All Providers' },
  { value: 'claude', label: 'Anthropic Claude', provider: 'claude' },
  { value: 'opencode', label: 'OpenCode', provider: 'opencode' },
];

describe('MenuSelect', () => {
  it('shows the selected option on the trigger, not the first one', () => {
    // A picker that reads "All Providers" while the pane filters to OpenCode is
    // a filter the user cannot see the effect of.
    render(
      <MenuSelect
        label="Provider"
        value="opencode"
        onChange={() => {}}
        options={OPTIONS}
        ariaLabel="Gateway provider"
      />,
    );
    expect(screen.getByRole('combobox')).toHaveTextContent('OpenCode');
  });

  it('marks each provider with its brand, the way macOS bridges NSPopUpButton', async () => {
    // `BrandIcons.swift:41`: SwiftUI's Picker drops custom image labels from
    // NSMenu items, so the brand mark is the reason this control exists.
    const user = userEvent.setup();
    render(
      <MenuSelect
        label="Provider"
        value=""
        onChange={() => {}}
        options={OPTIONS}
        ariaLabel="Gateway provider"
      />,
    );
    await user.click(screen.getByRole('combobox'));
    const list = within(screen.getByRole('listbox'));
    expect(list.getAllByRole('option').map((o) => o.textContent)).toEqual([
      '✓All Providers',
      'Anthropic Claude',
      'OpenCode',
    ]);
    // opencode.svg is a single-fill mark, so it arrives as a CSS mask rather
    // than an <img> — that is how a webview does what `image.isTemplate` does.
    const mark = list.getByRole('option', { name: 'OpenCode' }).firstElementChild
      ?.nextElementSibling as HTMLElement;
    expect(mark.style.maskImage).toContain('opencode.svg');
  });

  it('checkmarks the selected item and reports the chosen value', async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(
      <MenuSelect
        label="Provider"
        value=""
        onChange={onChange}
        options={OPTIONS}
        ariaLabel="Gateway provider"
      />,
    );
    await user.click(screen.getByRole('combobox'));
    expect(screen.getByRole('option', { selected: true })).toHaveTextContent('All Providers');

    await user.click(screen.getByRole('option', { name: 'Anthropic Claude' }));
    expect(onChange).toHaveBeenCalledWith('claude');
    // Closing is what a platform menu does, and focus comes back to the trigger
    // so the next keystroke reaches the control rather than the page.
    expect(screen.queryByRole('listbox')).toBeNull();
  });

  it('closes on Escape without choosing anything', async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(
      <MenuSelect label="Provider" value="" onChange={onChange} options={OPTIONS} ariaLabel="p" />,
    );
    await user.click(screen.getByRole('combobox'));
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('listbox')).toBeNull();
    expect(onChange).not.toHaveBeenCalled();
  });

  it('is one control that cannot be opened while disabled', async () => {
    const user = userEvent.setup();
    render(<MenuSelect label="Provider" value="" onChange={() => {}} options={OPTIONS} disabled />);
    const trigger = screen.getByRole('combobox');
    expect(trigger).toBeDisabled();
    await user.click(trigger);
    expect(screen.queryByRole('listbox')).toBeNull();
  });
});
