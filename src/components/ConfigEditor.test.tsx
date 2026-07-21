import React from 'react';
import { render, screen } from '@testing-library/react';
import { ConfigEditor } from './ConfigEditor';

const options = {
  jsonData: { accountName: 'acct' },
  secureJsonFields: { connectionString: true },
  secureJsonData: {},
} as never;

describe('ConfigEditor', () => {
  it('renders account name and a configured secret field', () => {
    render(<ConfigEditor options={options} onOptionsChange={jest.fn()} />);
    expect(screen.getByDisplayValue('acct')).toBeInTheDocument();
    expect(screen.getByText(/Reset/i)).toBeInTheDocument(); // SecretInput configured state
  });
});
