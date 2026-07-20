import React from 'react';
import { QueryEditorProps, SelectableValue } from '@grafana/data';
import { CodeEditor, InlineField, RadioButtonGroup } from '@grafana/ui';
import { DataSource } from '../datasource';
import { ParquetBlobDataSourceOptions, ParquetBlobQuery, QueryFormat } from '../types';

type Props = QueryEditorProps<DataSource, ParquetBlobQuery, ParquetBlobDataSourceOptions>;

const FORMATS: Array<SelectableValue<QueryFormat>> = [
  { label: 'Table', value: 'table' },
  { label: 'Time series', value: 'timeseries' },
];

export function QueryEditor({ query, onChange, onRunQuery }: Props) {
  return (
    <div>
      <CodeEditor
        language="sql"
        height="200px"
        value={query.rawSql ?? ''}
        showMiniMap={false}
        showLineNumbers={true}
        onBlur={(rawSql) => onChange({ ...query, rawSql })}
        onSave={(rawSql) => {
          onChange({ ...query, rawSql });
          onRunQuery();
        }}
      />
      <InlineField label="Format" labelWidth={10}>
        <RadioButtonGroup
          options={FORMATS}
          value={query.format ?? 'table'}
          onChange={(format) => {
            onChange({ ...query, format });
            onRunQuery();
          }}
        />
      </InlineField>
    </div>
  );
}
