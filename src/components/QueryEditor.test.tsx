import React from 'react';
import { render, screen } from '@testing-library/react';
import { QueryEditor } from './QueryEditor';

// CodeEditor pulls in Monaco, which jsdom can't run — stub it.
jest.mock('@grafana/ui', () => ({
  ...jest.requireActual('@grafana/ui'),
  CodeEditor: (props: { value: string }) => <textarea data-testid="code-editor" defaultValue={props.value} />,
}));

const props = {
  query: { refId: 'A', rawSql: 'SELECT 1', format: 'table' as const },
  onChange: jest.fn(),
  onRunQuery: jest.fn(),
  datasource: {} as never,
};

describe('QueryEditor', () => {
  it('renders the SQL editor with the query text and both formats', () => {
    render(<QueryEditor {...props} />);
    expect(screen.getByTestId('code-editor')).toHaveValue('SELECT 1');
    expect(screen.getByLabelText('Table')).toBeInTheDocument();
    expect(screen.getByLabelText('Time series')).toBeInTheDocument();
  });
});
