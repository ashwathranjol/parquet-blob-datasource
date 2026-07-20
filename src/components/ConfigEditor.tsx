import React, { ChangeEvent } from 'react';
import { DataSourcePluginOptionsEditorProps } from '@grafana/data';
import { InlineField, Input, SecretInput } from '@grafana/ui';
import { ParquetBlobDataSourceOptions, ParquetBlobSecureJsonData } from '../types';

interface Props
  extends DataSourcePluginOptionsEditorProps<ParquetBlobDataSourceOptions, ParquetBlobSecureJsonData> {}

export function ConfigEditor(props: Props) {
  const { onOptionsChange, options } = props;
  const { jsonData, secureJsonFields, secureJsonData } = options;

  const onAccountNameChange = (e: ChangeEvent<HTMLInputElement>) =>
    onOptionsChange({ ...options, jsonData: { ...jsonData, accountName: e.target.value } });

  const onMaxRowsChange = (e: ChangeEvent<HTMLInputElement>) =>
    onOptionsChange({
      ...options,
      jsonData: { ...jsonData, maxRows: e.target.value ? Number(e.target.value) : undefined },
    });

  const onConnectionStringChange = (e: ChangeEvent<HTMLInputElement>) =>
    onOptionsChange({
      ...options,
      secureJsonData: { ...secureJsonData, connectionString: e.target.value },
    });

  const onResetConnectionString = () =>
    onOptionsChange({
      ...options,
      secureJsonFields: { ...secureJsonFields, connectionString: false },
      secureJsonData: { ...secureJsonData, connectionString: '' },
    });

  return (
    <>
      <InlineField label="Storage account" labelWidth={22} tooltip="Azure storage account name">
        <Input
          id="config-editor-account-name"
          value={jsonData.accountName ?? ''}
          onChange={onAccountNameChange}
          placeholder="mystorageaccount"
          width={40}
        />
      </InlineField>
      <InlineField
        label="Connection string"
        labelWidth={22}
        tooltip="Stored encrypted; never sent back to the browser"
      >
        <SecretInput
          id="config-editor-connection-string"
          isConfigured={!!secureJsonFields.connectionString}
          value={secureJsonData?.connectionString ?? ''}
          onChange={onConnectionStringChange}
          onReset={onResetConnectionString}
          placeholder="DefaultEndpointsProtocol=https;AccountName=...;AccountKey=..."
          width={40}
        />
      </InlineField>
      <InlineField label="Max rows" labelWidth={22} tooltip="Row cap per query (default 1,000,000)">
        <Input
          id="config-editor-max-rows"
          type="number"
          value={jsonData.maxRows ?? ''}
          onChange={onMaxRowsChange}
          placeholder="1000000"
          width={40}
        />
      </InlineField>
    </>
  );
}
