import { DataSourcePlugin } from '@grafana/data';
import { DataSource } from './datasource';
import { ConfigEditor } from './components/ConfigEditor';
import { QueryEditor } from './components/QueryEditor';
import { ParquetBlobQuery, ParquetBlobDataSourceOptions } from './types';

export const plugin = new DataSourcePlugin<DataSource, ParquetBlobQuery, ParquetBlobDataSourceOptions>(DataSource)
  .setConfigEditor(ConfigEditor)
  .setQueryEditor(QueryEditor);
